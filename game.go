// Package main implements GoIdle, a standalone IdleRPG IRC bot written in Go.
//
// This file contains the core game engine: player and quest data types, the
// per-second tick loop, all battle mechanics, random events, the grid/map
// system, persistence, and every player-facing command handler.
//
// # Concurrency model
//
// A single [sync.Mutex] (Game.mu) protects all mutable state. The tick
// goroutine holds mu for the computation phase, then releases it before
// sending IRC messages. Command handlers follow the same pattern: acquire mu,
// mutate state and collect messages, release mu, then send. Functions annotated
// "Must be called with mu held" must never call say/setTopic or acquire mu
// again (deadlock). Functions annotated "Must NOT be called while holding mu"
// call updateTopic or say and must therefore be invoked after releasing the lock.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	mathrand "math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// itemSlots names the ten equipment slots in display order. The slice index is
// used everywhere items are stored (Player.Items, Player.ItemNames).
var itemSlots = [10]string{
	"ring", "amulet", "charm", "weapon", "helm",
	"tunic", "gloves", "leggings", "shield", "boots",
}

// Random-event message templates. Each uses fmt.Sprintf with (nick, pct) args.
var calamityMsgs = []string{
	"%s is forsaken by their deity! TTL increased by %d%%.",
	"A wandering curse latches onto %s. TTL increased by %d%%.",
	"%s trips over a root and loses precious time. TTL increased by %d%%.",
	"Fate frowns upon %s. TTL increased by %d%%.",
	"A black cat crosses %s's path. TTL increased by %d%%.",
	"%s is visited by ill omens. TTL increased by %d%%.",
}

var godsendMsgs = []string{
	"The gods smile upon %s! TTL reduced by %d%%.",
	"A ray of divine light blesses %s! TTL reduced by %d%%.",
	"%s finds a shortcut on the road to glory! TTL reduced by %d%%.",
	"Fortune favours %s today! TTL reduced by %d%%.",
	"A celestial wind carries %s forward! TTL reduced by %d%%.",
	"%s receives a blessing from the heavens! TTL reduced by %d%%.",
}

// Item-event templates use (nick, slotName, pct) args.
var itemCalamityMsgs = []string{
	"%s's %s was damaged in an ambush! Item level reduced by %d%%.",
	"A thief nicks %s's %s in the marketplace! Item level reduced by %d%%.",
	"%s drops their %s down a well. Item level reduced by %d%%.",
	"Rust claims %s's %s. Item level reduced by %d%%.",
}

var itemGodsendMsgs = []string{
	"%s polishes their %s to a shine! Item level increased by %d%%.",
	"A wandering smith improves %s's %s! Item level increased by %d%%.",
	"Divine favour enchants %s's %s! Item level increased by %d%%.",
	"%s finds a rare component and upgrades their %s! Item level increased by %d%%.",
}

// handOfGodMsgs[0] = hurt templates, handOfGodMsgs[1] = help templates.
// Each uses (nick, pct) args.
var handOfGodMsgs = [2][]string{
	{
		"The hand of %s's god reaches down and sets them back %d%%!",
		"%s has displeased their deity — struck back %d%%!",
		"A divine rebuke sends %s stumbling backward %d%%!",
	},
	{
		"The hand of %s's god reaches down and pushes them forward %d%%!",
		"%s basks in divine favour and surges ahead %d%%!",
		"A celestial nudge propels %s forward %d%%!",
	},
}

// Alignment constants. The int8 value is stored in Player.Alignment and
// affects battle power, crit chance, and daily events.
const (
	AlignEvil    int8 = -1
	AlignNeutral int8 = 0
	AlignGood    int8 = 1
)

// alignNames maps the numeric alignment to its display string.
var alignNames = map[int8]string{
	AlignEvil:    "evil",
	AlignNeutral: "neutral",
	AlignGood:    "good",
}

// Good-alignment event templates use (nick1, nick2, pct) args (the triggering
// player is nick1; their randomly chosen partner is nick2).
var goodEventMsgs = []string{
	"The light of %s's god shines upon %s and %s! Both surge ahead %d%%.",
	"%s and %s are united by divine favour! Both gain %d%%.",
	"The gods bless the fellowship of %s and %s! Both advance %d%%.",
}

// Evil steal templates use (evilNick, victimNick, slotName, itemLevel) args.
var evilStealMsgs = []string{
	"%s lurks in the shadows and makes off with %s's %s (level %d)!",
	"%s bribes a corrupt merchant to acquire %s's %s (level %d)!",
	"Under cover of darkness, %s pilfers %s's %s (level %d)!",
}

// forsakenMsgs are used when an evil player has no good target to steal from,
// or loses the 50/50 steal roll. Args: (nick, pct).
var forsakenMsgs = []string{
	"%s is forsaken by their dark patron! TTL increased by %d%%.",
	"The shadows abandon %s. TTL increased by %d%%.",
	"%s's evil deeds catch up with them. TTL increased by %d%%.",
}

// questDescs are the flavour descriptions attached to quests.
var questDescs = []string{
	"slay the dragon terrorising the village of Mal'Gorn",
	"recover the stolen Orb of Aldur from the goblin warrens",
	"escort the merchant caravan through the Darkwood",
	"retrieve the ancient tome from the sunken library",
	"defeat the lich haunting the catacombs beneath Castle Greystone",
	"find the missing children taken by the forest sprites",
	"seal the dimensional rift opening near the city of Varek",
	"break the curse on the village of Mirewood",
	"purge the corrupted well poisoning the town of Ashfen",
	"hunt down the bandit king who plagues the northern roads",
	"recover the holy relic stolen from the Temple of Aeon",
	"investigate the strange lights appearing over the Grimfen swamp",
}

// Quest eligibility thresholds.
const questMinLevel = 15  // minimum player level to be chosen as a quester
const questMinPlayers = 4 // number of questers required to start a quest

// gridSize is the side length of the toroidal map in tiles. Players wrap
// around at the edges, so the effective space is always gridSize×gridSize.
const gridSize = 500

// Quest holds the state of an in-progress quest. Only one quest can be active
// at a time (stored in Game.quest).
type Quest struct {
	// Questers are the players chosen to complete this quest.
	Questers []*Player
	// EndsAt is when the quest times out. For grid quests it is also the
	// deadline by which all questers must reach (QX, QY).
	EndsAt time.Time
	// Desc is the human-readable quest objective used in announcements.
	Desc string
	// OnlineAtStart records which players (lowercase nicks) were online when
	// the quest began. Only these players are penalised on failure, preventing
	// late-joiners from being punished for a quest they had no part in.
	OnlineAtStart map[string]bool
	// IsGrid distinguishes grid quests (must reach a coordinate) from time
	// quests (must simply stay online until the timer expires).
	IsGrid bool
	// QX, QY are the target coordinates for grid quests.
	QX, QY int
	// Reached tracks which questers (lowercase nicks) have stepped onto
	// (QX, QY). The quest resolves as soon as len(Reached) == len(Questers).
	Reached map[string]bool
}

// Player represents a registered IdleRPG character. It is persisted to JSON
// and keyed by lowercase nick in Game.players.
type Player struct {
	Nick   string // display nick, case-preserved
	Class  string // primary class, free-form text chosen at registration
	Class2 string // secondary class chosen via !dualclass at level 12+; empty if not dual-classed

	// Password is stored as a salted SHA-256 hash. PassSalt is a 16-byte
	// random value encoded as 32 hex characters. This prevents rainbow-table
	// attacks if the JSON file is ever leaked.
	PassSalt string
	PassHash string

	// Alignment affects battle power (+/-10%), crit chance, and daily events.
	Alignment int8

	Level int
	// TTL is seconds until the next level-up. It decrements by 1 every tick
	// and is increased by penalties and random calamities.
	TTL int64

	// Items holds the item level for each of the ten equipment slots. A value
	// of 0 means the slot is empty.
	Items [10]int
	// ItemNames holds the procedurally generated name for Uncommon/Rare/
	// Legendary items. An empty string means the slot holds a plain item.
	ItemNames [10]string

	Online bool   // true while the player is connected and logged in
	Addr   string // full nick!user@host mask used to identify the player on IRC

	// X, Y are the player's current position on the toroidal grid. They are
	// randomised on each login and are not persisted (position resets on reconnect).
	X, Y int
}

// itemSum returns the total of all item slot levels, used as the base value
// in battle calculations before focus-slot and alignment bonuses are applied.
func (p *Player) itemSum() int {
	s := 0
	for _, v := range p.Items {
		s += v
	}
	return s
}

// Game is the central game state. All fields except DevMode are protected by mu.
type Game struct {
	// players maps lowercase nick to Player. It is the authoritative player
	// store; all lookups and mutations go through this map under mu.
	players map[string]*Player
	// guilds maps lowercase guild name to Guild.
	guilds map[string]*Guild

	mu sync.Mutex

	dataFile   string // path to the player JSON save file
	guildsFile string // path to the guild JSON save file

	// say sends a message to the game channel. Provided by main so the game
	// engine is not coupled to a specific IRC library.
	say func(string)
	// setTopic sets the channel topic. Wired by main after construction.
	setTopic func(string)

	// lastEvent is a short description of the most recent notable game event,
	// appended to the channel topic. Updated under mu; read outside mu.
	lastEvent string

	// stopTick is closed to stop the current tick goroutine. A new channel is
	// created and a new goroutine launched on each call to start(), which
	// prevents goroutine leaks across reconnects.
	stopTick chan struct{}

	// quest holds the active quest, or nil when none is running.
	quest *Quest

	// DevMode speeds up TTL by 5× and auto-logins existing channel members on
	// connect. Set before start() is called; never mutated under mu.
	DevMode bool

	// Rates controls how frequently various random events fire. Each field is a
	// multiplier relative to the default rate: 1.0 = normal, 2.0 = twice as
	// often, 0.5 = half as often. Set before start() is called; never mutated
	// under mu.
	Rates Rates
}

// Rates holds per-category event frequency multipliers. A value of 1.0 means
// the default rate; higher values increase frequency proportionally.
type Rates struct {
	// PlayerEvents scales per-player random events and bot-battle challenges
	// (default: ~1/day each).
	PlayerEvents float64
	// AlignmentEvents scales good- and evil-alignment daily events
	// (default: good ~1/12 days, evil ~1/8 days).
	AlignmentEvents float64
	// ServerEvents scales team battles, guild battles, quests, and Hand of God
	// (default rates vary; see tickServerEvents).
	ServerEvents float64
}

// defaultRates returns a Rates with all multipliers set to 1.0.
func defaultRates() Rates {
	return Rates{PlayerEvents: 1.0, AlignmentEvents: 1.0, ServerEvents: 1.0}
}

// rateCheck returns true with probability (multiplier/denominator) per call.
// It is equivalent to mathrand.Intn(denominator)==0 when multiplier==1.0.
// The effective denominator is clamped to a minimum of 1 so the result is
// always valid regardless of how large the multiplier is.
func rateCheck(denominator int, multiplier float64) bool {
	if multiplier <= 0 {
		return false
	}
	n := int(float64(denominator) / multiplier)
	if n < 1 {
		n = 1
	}
	return mathrand.Intn(n) == 0
}

// newGame creates a Game, loads persisted player and guild data, and wires the
// say function. setTopic must be assigned by the caller before start().
func newGame(dataFile, guildsFile string, say func(string)) *Game {
	g := &Game{
		players:    make(map[string]*Player),
		guilds:     make(map[string]*Guild),
		dataFile:   dataFile,
		guildsFile: guildsFile,
		say:        say,
		Rates:      defaultRates(),
	}
	g.load()
	g.loadGuilds()
	return g
}

// start stops any running tick goroutine, then launches a fresh one and
// refreshes the channel topic. Called on every successful IRC connect.
func (g *Game) start() {
	if g.stopTick != nil {
		close(g.stopTick)
	}
	g.stopTick = make(chan struct{})
	go g.tick(g.stopTick)
	g.updateTopic()
}

// OnJoin auto-logs in a registered player when they join the channel and
// announces their return. Unregistered joiners are silently ignored.
func (g *Game) OnJoin(src string) {
	nick := extractNick(src)
	g.mu.Lock()
	p := g.players[strings.ToLower(nick)]
	if p != nil {
		p.Online = true
		p.Addr = src
		// Position is randomised on every login so players cannot farm
		// encounters by repeatedly quitting and rejoining near a target.
		p.X = mathrand.Intn(gridSize)
		p.Y = mathrand.Intn(gridSize)
	}
	g.mu.Unlock()
	if p != nil {
		g.save()
		g.say(fmt.Sprintf("%s, the level %d %s, has joined IdleRPG at (%d,%d)! Next level in %s.",
			p.Nick, p.Level, p.Class, p.X, p.Y, fmtDuration(p.TTL)))
		g.noteEvent(fmt.Sprintf("%s (lvl %d) joined", p.Nick, p.Level))
	}
}

// OnPart applies a p200 penalty and marks the player offline.
func (g *Game) OnPart(src string) {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p != nil {
		g.applyPenalty(p, 200)
		p.Online = false
	}
	g.mu.Unlock()
	if p != nil {
		g.save()
		g.updateTopic()
	}
}

// OnQuit applies a p20 penalty and marks the player offline. It first tries to
// find the player by their full addr (nick!user@host); if that fails it falls
// back to nick-only lookup to handle servers that omit the host on QUIT.
func (g *Game) OnQuit(src string) {
	nick := extractNick(src)
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		p = g.players[strings.ToLower(nick)]
		if p != nil && !p.Online {
			p = nil
		}
	}
	if p != nil {
		g.applyPenalty(p, 20)
		p.Online = false
	}
	g.mu.Unlock()
	if p != nil {
		g.save()
		g.updateTopic()
	}
}

// OnNick applies a p30 penalty, re-keys the player in the map under the new
// nick, and updates any guild membership or leadership records that reference
// the old nick.
func (g *Game) OnNick(src, newNick string) {
	oldNick := extractNick(src)
	oldKey := strings.ToLower(oldNick)
	newKey := strings.ToLower(newNick)
	g.mu.Lock()
	p := g.players[oldKey]
	if p != nil && p.Online {
		g.applyPenalty(p, 30)
		delete(g.players, oldKey)
		p.Nick = newNick
		// Addr is stored as "nick!user@host"; replace only the nick portion.
		p.Addr = strings.Replace(p.Addr, oldNick, newNick, 1)
		g.players[newKey] = p
		if guild := g.playerGuild(oldKey); guild != nil {
			for i, m := range guild.Members {
				if m == oldKey {
					guild.Members[i] = newKey
					break
				}
			}
			if guild.Leader == oldKey {
				guild.Leader = newKey
			}
		}
	} else {
		p = nil
	}
	g.mu.Unlock()
	if p != nil {
		g.save()
		g.saveGuilds()
	}
}

// OnKick applies a p50 penalty and marks the kicked player offline.
func (g *Game) OnKick(kickedNick string) {
	g.mu.Lock()
	p := g.players[strings.ToLower(kickedNick)]
	if p != nil && p.Online {
		g.applyPenalty(p, 50)
		p.Online = false
	} else {
		p = nil
	}
	g.mu.Unlock()
	if p != nil {
		g.save()
	}
}

// OnPrivmsg applies a talk penalty of 1 second per character of the message.
// Called for every PRIVMSG in the game channel, including commands.
func (g *Game) OnPrivmsg(src, text string) {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p != nil {
		g.applyPenalty(p, int64(len(text)))
	}
	g.mu.Unlock()
	if p != nil {
		g.save()
	}
}

// CmdRegister creates a new character with the given nick, class, and password.
// The registration result is announced publicly; the password itself is never
// echoed. Returns an error string if the nick is already taken or inputs are
// out of range.
func (g *Game) CmdRegister(src, nick, class, pass string) string {
	if len(nick) == 0 || len(nick) > 30 {
		return "Nick must be 1–30 characters."
	}
	if len(class) == 0 || len(class) > 50 {
		return "Class must be 1–50 characters."
	}
	key := strings.ToLower(nick)
	salt := newSalt()
	p := &Player{
		Nick:     nick,
		Class:    class,
		PassSalt: salt,
		PassHash: hashPass(salt, pass),
		Level:    0,
		TTL:      g.ttlForLevel(0),
	}
	// Hold the lock across both the existence check and the insert to prevent
	// two concurrent !register calls from creating duplicate nicks (TOCTOU).
	g.mu.Lock()
	_, exists := g.players[key]
	if !exists {
		g.players[key] = p
	}
	g.mu.Unlock()
	if exists {
		return fmt.Sprintf("Nick %s is already registered.", nick)
	}
	g.save()
	return fmt.Sprintf("%s, the %s, has registered for IdleRPG! Next level in %s.", nick, class, fmtDuration(p.TTL))
}

// CmdLogin authenticates the player whose current IRC nick matches a registered
// character. The response is sent privately to avoid leaking "Wrong password."
// to the channel.
func (g *Game) CmdLogin(src, pass string) string {
	nick := extractNick(src)
	key := strings.ToLower(nick)
	g.mu.Lock()
	p, ok := g.players[key]
	g.mu.Unlock()
	if !ok {
		return "No character registered with that nick. Use !register <nick> <class> <pass> first."
	}
	// Use constant-time comparison to avoid leaking password length or prefix
	// information through timing differences.
	if subtle.ConstantTimeCompare([]byte(p.PassHash), []byte(hashPass(p.PassSalt, pass))) != 1 {
		return "Wrong password."
	}
	g.mu.Lock()
	p.Online = true
	p.Addr = src
	g.mu.Unlock()
	g.save()
	return fmt.Sprintf("%s, the level %d %s, has logged in! Next level in %s.", nick, p.Level, p.Class, fmtDuration(p.TTL))
}

// CmdLogout marks the calling player offline. No penalty is applied.
func (g *Game) CmdLogout(src string) string {
	nick := extractNick(src)
	g.mu.Lock()
	p := g.findByAddr(src)
	if p != nil {
		p.Online = false
	}
	g.mu.Unlock()
	if p == nil {
		return "You are not logged in."
	}
	g.save()
	return fmt.Sprintf("%s has logged out of IdleRPG.", nick)
}

// CmdAlign sets the calling player's alignment. Changing alignment (not just
// confirming the current one) costs a p75 penalty.
func (g *Game) CmdAlign(src, align string) string {
	var newAlign int8
	switch strings.ToLower(align) {
	case "good":
		newAlign = AlignGood
	case "evil":
		newAlign = AlignEvil
	case "neutral":
		newAlign = AlignNeutral
	default:
		return "Usage: !align <good|neutral|evil>"
	}
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	changed := p.Alignment != newAlign
	p.Alignment = newAlign
	if changed {
		g.applyPenalty(p, 75)
	}
	g.mu.Unlock()
	g.save()
	if changed {
		return fmt.Sprintf("%s is now %s. Changing alignment costs time — TTL adjusted.", p.Nick, alignNames[newAlign])
	}
	return fmt.Sprintf("%s is already %s.", p.Nick, alignNames[newAlign])
}

// CmdDualClass lets a player at level 12+ permanently choose a second class.
// The second class adds an additional focus-slot bonus in all battle rolls.
func (g *Game) CmdDualClass(src, class string) string {
	class = strings.TrimSpace(class)
	if class == "" {
		return "Usage: !dualclass <class>"
	}
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	if p.Level < 12 {
		g.mu.Unlock()
		return fmt.Sprintf("You must be at least level 12 to dual-class (you are level %d).", p.Level)
	}
	if p.Class2 != "" {
		g.mu.Unlock()
		return fmt.Sprintf("You are already dual-classed as %s/%s.", p.Class, p.Class2)
	}
	p.Class2 = class
	slot1 := classFocusSlot(p.Class)
	slot2 := classFocusSlot(p.Class2)
	nick := p.Nick
	g.mu.Unlock()
	g.save()
	if slot1 == slot2 {
		return fmt.Sprintf("%s is now a %s/%s! Both classes share the %s focus — that slot counts triple in battle.",
			nick, p.Class, class, itemSlots[slot1])
	}
	return fmt.Sprintf("%s is now a %s/%s! Primary focus: %s. Secondary focus: %s. Both count double in battle.",
		nick, p.Class, class, itemSlots[slot1], itemSlots[slot2])
}

// CmdStatus returns a one-line status summary for the target player. If
// targetNick is empty, it reports on the calling player.
func (g *Game) CmdStatus(src, targetNick string) string {
	if targetNick == "" {
		targetNick = extractNick(src)
	}
	g.mu.Lock()
	p, ok := g.players[strings.ToLower(targetNick)]
	g.mu.Unlock()
	if !ok {
		return fmt.Sprintf("No character found for %s.", targetNick)
	}
	status := "offline"
	if p.Online {
		status = "online"
	}
	// Check whether the player is an active quester.
	questInfo := ""
	g.mu.Lock()
	if g.quest != nil {
		for _, qp := range g.quest.Questers {
			if qp == p {
				questInfo = fmt.Sprintf(" [on quest, ends in %s]", fmtDuration(int64(time.Until(g.quest.EndsAt).Seconds())))
				break
			}
		}
	}
	g.mu.Unlock()
	classDisplay := p.Class
	focusDisplay := itemSlots[classFocusSlot(p.Class)]
	if p.Class2 != "" {
		classDisplay = p.Class + "/" + p.Class2
		slot2 := itemSlots[classFocusSlot(p.Class2)]
		if slot2 == focusDisplay {
			focusDisplay += "×3"
		} else {
			focusDisplay += "+" + slot2
		}
	}
	return fmt.Sprintf("%s, the %s level %d %s [%s]%s — TTL: %s — Items: %d (focus: %s)",
		p.Nick, alignNames[p.Alignment], p.Level, classDisplay, status, questInfo,
		fmtDuration(p.TTL), p.itemSum(), focusDisplay)
}

// CmdPos returns the grid coordinates of the target player and lists any
// co-located players sharing the same tile. If targetNick is empty, reports
// on the calling player.
func (g *Game) CmdPos(src, targetNick string) string {
	if targetNick == "" {
		targetNick = extractNick(src)
	}
	g.mu.Lock()
	p, ok := g.players[strings.ToLower(targetNick)]
	if !ok {
		g.mu.Unlock()
		return fmt.Sprintf("No character found for %s.", targetNick)
	}
	if !p.Online {
		g.mu.Unlock()
		return fmt.Sprintf("%s is offline and has no position.", p.Nick)
	}
	x, y, nick := p.X, p.Y, p.Nick

	var neighbours []string
	for _, op := range g.players {
		if op != p && op.Online && op.X == x && op.Y == y {
			neighbours = append(neighbours, op.Nick)
		}
	}

	questNote := ""
	if g.quest != nil && g.quest.IsGrid && g.quest.QX == x && g.quest.QY == y {
		questNote = " [quest destination!]"
	}
	g.mu.Unlock()

	info := fmt.Sprintf("%s is at (%d,%d)%s on a %d×%d grid.", nick, x, y, questNote, gridSize, gridSize)
	if len(neighbours) > 0 {
		info += fmt.Sprintf(" Also here: %s.", strings.Join(neighbours, ", "))
	}
	return info
}

// CmdTop returns the top 5 players sorted by level descending, then by TTL
// ascending (closest to levelling up wins ties).
func (g *Game) CmdTop() string {
	g.mu.Lock()
	players := make([]*Player, 0, len(g.players))
	for _, p := range g.players {
		players = append(players, p)
	}
	g.mu.Unlock()

	sort.Slice(players, func(i, j int) bool {
		if players[i].Level != players[j].Level {
			return players[i].Level > players[j].Level
		}
		return players[i].TTL < players[j].TTL
	})

	n := 5
	if len(players) < n {
		n = len(players)
	}
	if n == 0 {
		return "No players yet."
	}
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		p := players[i]
		parts[i] = fmt.Sprintf("%d. %s (lvl %d, items %d)", i+1, p.Nick, p.Level, p.itemSum())
	}
	return "Top players: " + strings.Join(parts, " | ")
}

// CmdQuest returns a human-readable description of the active quest including
// questers, objective, type, and remaining time.
func (g *Game) CmdQuest() string {
	g.mu.Lock()
	q := g.quest
	g.mu.Unlock()

	if q == nil {
		return "No quest is currently active."
	}

	names := make([]string, len(q.Questers))
	for i, p := range q.Questers {
		names[i] = p.Nick
	}
	questers := strings.Join(names, ", ")

	if q.IsGrid {
		remaining := time.Until(q.EndsAt)
		reached := len(q.Reached)
		total := len(q.Questers)
		return fmt.Sprintf("Grid quest: %s must reach (%d,%d) to %s — %d/%d there, %s remaining.",
			questers, q.QX, q.QY, q.Desc, reached, total, fmtDuration(int64(remaining.Seconds())))
	}
	return fmt.Sprintf("Quest: %s are on a mission to %s — %s remaining.",
		questers, q.Desc, fmtDuration(int64(time.Until(q.EndsAt).Seconds())))
}

// CmdOnline returns a sorted list of all currently online players with their levels.
func (g *Game) CmdOnline() string {
	g.mu.Lock()
	var parts []string
	for _, p := range g.players {
		if p.Online {
			parts = append(parts, fmt.Sprintf("%s (lvl %d)", p.Nick, p.Level))
		}
	}
	g.mu.Unlock()

	if len(parts) == 0 {
		return "No players currently online."
	}
	sort.Strings(parts)
	return fmt.Sprintf("Online (%d): %s", len(parts), strings.Join(parts, ", "))
}

// tick is the main game loop. It fires once per second for as long as the stop
// channel remains open (closed by start() on reconnect).
func (g *Game) tick(stop <-chan struct{}) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		g.mu.Lock()
		online := g.onlinePlayers()

		levelUps, msgs := g.tickPlayers(online)
		encounterPairs, gridMsgs := g.tickGrid(online)
		msgs = append(msgs, gridMsgs...)
		msgs = append(msgs, g.tickQuestProgress(online)...)
		msgs = append(msgs, g.tickServerEvents(online)...)

		topicWorthy := len(levelUps) > 0 || len(encounterPairs) > 0
		if ev := g.captureNotableEvent(msgs); ev != "" {
			g.lastEvent = ev
			topicWorthy = true
		}

		g.mu.Unlock()

		for _, msg := range msgs {
			g.say(msg)
		}
		// Encounters trigger a standard 1v1 battle outside the lock because
		// battle() acquires mu internally.
		for _, ep := range encounterPairs {
			g.battle(ep[0], ep[1])
		}
		for _, p := range levelUps {
			g.doLevelUp(p)
		}
		if len(levelUps) > 0 {
			g.save()
		}
		if topicWorthy {
			g.updateTopic()
		}
	}
}

// tickPlayers decrements TTL for each online player, queues those whose TTL
// has reached zero for level-up, and fires per-player random/bot-battle/
// alignment events. Must be called with mu held.
func (g *Game) tickPlayers(online []*Player) (levelUps []*Player, msgs []string) {
	for _, p := range online {
		p.TTL--
		if p.TTL <= 0 {
			levelUps = append(levelUps, p)
			continue
		}
		// ~1/day: random individual event (calamity, godsend, item change, find item).
		if rateCheck(86400, g.Rates.PlayerEvents) {
			msgs = append(msgs, g.randomEvent(p))
		}
		// ~1/day: 1v1 challenge against the bot.
		if rateCheck(86400, g.Rates.PlayerEvents) {
			msgs = append(msgs, g.botBattle(p))
		}
		msgs = append(msgs, g.tickAlignmentEvent(p, online)...)
	}
	return
}

// tickAlignmentEvent fires an alignment-specific event for p with the
// appropriate per-alignment probability. Returns zero or one message.
// Must be called with mu held.
func (g *Game) tickAlignmentEvent(p *Player, online []*Player) []string {
	switch p.Alignment {
	case AlignGood:
		// ~once per 12 days: pair with another good player for a mutual TTL bonus.
		if rateCheck(86400*12, g.Rates.AlignmentEvents) {
			if m := g.goodAlignmentEvent(p, online); m != "" {
				return []string{m}
			}
		}
	case AlignEvil:
		// ~once per 8 days: steal from a good player or get forsaken.
		if rateCheck(86400*8, g.Rates.AlignmentEvents) {
			return []string{g.evilAlignmentEvent(p, online)}
		}
	}
	return nil
}

// tickGrid moves every online player one step in a random direction on the
// toroidal map and checks for co-tile encounters. Returns up to one encounter
// pair per tick (to prevent message flooding) and any encounter announcement
// messages. Must be called with mu held.
func (g *Game) tickGrid(online []*Player) (encounterPairs [][2]*Player, msgs []string) {
	// Build a position map after moving everyone.
	posMap := make(map[[2]int][]*Player, len(online))
	for _, p := range online {
		// ±1 step with toroidal wrap; +gridSize before mod prevents negative indices.
		p.X = (p.X + mathrand.Intn(3) - 1 + gridSize) % gridSize
		p.Y = (p.Y + mathrand.Intn(3) - 1 + gridSize) % gridSize
		key := [2]int{p.X, p.Y}
		posMap[key] = append(posMap[key], p)
	}

	// Encounter probability scales with the crowd: 1/len(online) per shared
	// tile, so larger populations see proportionally fewer surprise fights.
	if len(online) > 0 {
		for _, group := range posMap {
			if len(group) >= 2 && mathrand.Intn(len(online)) == 0 {
				mathrand.Shuffle(len(group), func(i, j int) { group[i], group[j] = group[j], group[i] })
				encounterPairs = append(encounterPairs, [2]*Player{group[0], group[1]})
				break // one encounter per tick to avoid flooding
			}
		}
	}
	if len(encounterPairs) > 0 {
		ep := encounterPairs[0]
		msgs = append(msgs, fmt.Sprintf("%s and %s stumble into each other at (%d,%d)!",
			ep[0].Nick, ep[1].Nick, ep[0].X, ep[0].Y))
	}
	return
}

// tickQuestProgress checks whether any grid-quest questers have stepped onto
// the target tile and resolves the quest immediately when all arrive.
// Must be called with mu held.
func (g *Game) tickQuestProgress(online []*Player) []string {
	if g.quest == nil || !g.quest.IsGrid {
		return nil
	}
	var msgs []string
	for _, qp := range g.quest.Questers {
		nick := strings.ToLower(qp.Nick)
		if !g.quest.Reached[nick] && qp.X == g.quest.QX && qp.Y == g.quest.QY {
			g.quest.Reached[nick] = true
			msgs = append(msgs, fmt.Sprintf("%s has reached the quest destination (%d,%d)!",
				qp.Nick, g.quest.QX, g.quest.QY))
		}
	}
	if len(g.quest.Reached) == len(g.quest.Questers) {
		msgs = append(msgs, g.resolveQuest(online)...)
		g.quest = nil
	}
	return msgs
}

// tickServerEvents fires the server-wide periodic events: Hand of God (~1/20
// days), team battle (~4/day when 6+ online), guild battle (~1/day), quest
// start (~1/day), and quest timeout resolution. Must be called with mu held.
func (g *Game) tickServerEvents(online []*Player) []string {
	var msgs []string
	if len(online) > 0 && rateCheck(86400*20, g.Rates.ServerEvents) {
		msgs = append(msgs, g.handOfGod(online[mathrand.Intn(len(online))]))
	}
	if len(online) >= 6 && rateCheck(86400/4, g.Rates.ServerEvents) {
		msgs = append(msgs, g.teamBattle(online)...)
	}
	if rateCheck(86400, g.Rates.ServerEvents) {
		msgs = append(msgs, g.guildBattle()...)
	}
	if g.quest == nil && rateCheck(86400, g.Rates.ServerEvents) {
		msgs = append(msgs, g.tryStartQuest(online)...)
	}
	if g.quest != nil && time.Now().After(g.quest.EndsAt) {
		msgs = append(msgs, g.resolveQuest(online)...)
		g.quest = nil
	}
	return msgs
}

// captureNotableEvent scans msgs for one worth recording as the channel topic's
// "last event" line. Returns the first matching message trimmed to 80 characters,
// or "" if none qualify. Must be called with mu held.
func (g *Game) captureNotableEvent(msgs []string) string {
	for _, m := range msgs {
		if isNotableEvent(m) {
			if len(m) > 80 {
				m = m[:77] + "..."
			}
			return m
		}
	}
	return ""
}

// isNotableEvent reports whether msg describes an event significant enough to
// display in the channel topic.
func isNotableEvent(m string) bool {
	return strings.Contains(m, "Quest") || strings.Contains(m, "quest") ||
		strings.Contains(m, "Guild battle") || strings.Contains(m, "Team battle") ||
		strings.Contains(m, "hand of") || strings.Contains(m, "Hand of") ||
		strings.Contains(m, "god") || strings.Contains(m, "LEGENDARY")
}

// onlinePlayers returns a snapshot of all currently online players.
// Must be called with mu held.
func (g *Game) onlinePlayers() []*Player {
	out := make([]*Player, 0, len(g.players))
	for _, p := range g.players {
		if p.Online {
			out = append(out, p)
		}
	}
	return out
}

// doLevelUp increments p's level, rolls a new item drop, announces the
// level-up, and triggers a 1v1 battle against a random online opponent.
// Called outside the lock; acquires mu internally for state mutations.
func (g *Game) doLevelUp(p *Player) {
	g.mu.Lock()
	p.Level++
	p.TTL = g.ttlForLevel(p.Level)

	slot, itemLevel, itemName, itemRarity := rollItemDrop(p)
	improved := itemLevel > p.Items[slot]
	if improved {
		p.Items[slot] = itemLevel
		p.ItemNames[slot] = itemName
	}
	slotName := itemSlots[slot]
	nick := p.Nick
	level := p.Level
	ttl := p.TTL
	isum := p.itemSum()

	// Collect eligible opponents while the lock is held.
	online := g.onlinePlayers()
	var opponents []*Player
	for _, op := range online {
		if strings.ToLower(op.Nick) != strings.ToLower(nick) {
			opponents = append(opponents, op)
		}
	}
	g.mu.Unlock()

	itemDesc := slotName
	if itemName != "" {
		itemDesc = fmt.Sprintf("%s (%s)", itemName, slotName)
	}
	equipped := ""
	if improved {
		equipped = " (equipped!)"
	}
	label := ""
	if itemRarity != rarityNormal {
		label = " " + rarityLabel(itemRarity)
	}
	g.say(fmt.Sprintf("%s has attained level %d! Next level in %s. They find a %s of level %d%s%s [item total: %d].",
		nick, level, fmtDuration(ttl), itemDesc, itemLevel, equipped, label, isum))

	switch itemRarity {
	case rarityLegendary:
		g.noteEvent(fmt.Sprintf("✦ %s found %s — LEGENDARY!", nick, itemName))
	case rarityRare:
		g.noteEvent(fmt.Sprintf("★ %s found %s (Rare) at lvl %d", nick, itemName, level))
	case rarityUncommon:
		g.noteEvent(fmt.Sprintf("%s reached lvl %d, found %s", nick, level, itemName))
	default:
		g.noteEvent(fmt.Sprintf("%s reached lvl %d", nick, level))
	}

	if len(opponents) > 0 {
		g.battle(p, opponents[mathrand.Intn(len(opponents))])
	}
}

// battle runs a standard 1v1 fight between a and b. Each side rolls
// rand(0, effectiveItemSum); the higher roll wins. The TTL swing is
// max(loser.Level/4, 7)% and is doubled on a critical hit. The winner has a
// 3% chance to steal one item slot from the loser. Acquires mu internally.
func (g *Game) battle(a, b *Player) {
	g.mu.Lock()

	// alignBonus adjusts a player's effective item sum: good +10%, evil -10%.
	alignBonus := func(p *Player, sum int) int {
		switch p.Alignment {
		case AlignGood:
			return sum + sum/10
		case AlignEvil:
			return sum - sum/10
		}
		return sum
	}

	aSum := alignBonus(a, effectiveItemSum(a))
	bSum := alignBonus(b, effectiveItemSum(b))
	// Clamp to 1 so mathrand.Intn never panics on a player with no items.
	if aSum < 1 {
		aSum = 1
	}
	if bSum < 1 {
		bSum = 1
	}

	aRoll := mathrand.Intn(aSum)
	bRoll := mathrand.Intn(bSum)

	winner, loser := a, b
	wRoll, lRoll := aRoll, bRoll
	if bRoll > aRoll {
		winner, loser = b, a
		wRoll, lRoll = bRoll, aRoll
	}

	// Crit probabilities differ by alignment; a crit doubles the TTL swing.
	crit := false
	switch winner.Alignment {
	case AlignGood:
		crit = mathrand.Intn(50) == 0
	case AlignEvil:
		crit = mathrand.Intn(20) == 0
	}

	pct := int(math.Max(float64(loser.Level)/4.0, 7))
	if crit {
		pct *= 2
	}
	change := winner.TTL * int64(pct) / 100
	if change < 1 {
		change = 1
	}
	winner.TTL -= change
	if winner.TTL < 0 {
		winner.TTL = 0
	}
	loser.TTL += change

	wName, lName := winner.Nick, loser.Nick
	wSum, lSum := winner.itemSum(), loser.itemSum()

	stealMsg := g.tryStealItem(winner, loser)
	g.mu.Unlock()

	critNote := ""
	if crit {
		critNote = " Critical hit!"
	}
	g.say(fmt.Sprintf("%s [%d/%d] battles %s [%d/%d] and wins!%s TTL adjusted by %d%%.",
		wName, wRoll, wSum, lName, lRoll, lSum, critNote, pct))
	if stealMsg != "" {
		g.say(stealMsg)
	}
}

// botBattle pits p against the bot in a 1v1 fight. The bot's item sum is set
// to 1 + the highest effectiveItemSum across all registered players, making it
// a credible but beatable opponent at any stage of the game.
// Win: −20% TTL. Loss: +10% TTL. Must be called with mu held.
func (g *Game) botBattle(p *Player) string {
	botSum := 1
	for _, op := range g.players {
		if s := effectiveItemSum(op); s > botSum-1 {
			botSum = s + 1
		}
	}

	pSum := effectiveItemSum(p)
	if pSum < 1 {
		pSum = 1
	}

	pRoll := mathrand.Intn(pSum)
	botRoll := mathrand.Intn(botSum)

	if pRoll >= botRoll {
		change := p.TTL * 20 / 100
		if change < 1 {
			change = 1
		}
		p.TTL -= change
		if p.TTL < 1 {
			p.TTL = 1
		}
		return fmt.Sprintf("%s [%d/%d] challenges the bot [%d/%d] and wins! TTL reduced by 20%%.",
			p.Nick, pRoll, pSum, botRoll, botSum)
	}

	change := p.TTL * 10 / 100
	if change < 1 {
		change = 1
	}
	p.TTL += change
	return fmt.Sprintf("%s [%d/%d] challenges the bot [%d/%d] and loses! TTL increased by 10%%.",
		p.Nick, pRoll, pSum, botRoll, botSum)
}

// tryStealItem gives the winner a 3% chance to take one item slot from the
// loser. If the stolen item is better than what the winner already has in that
// slot it is equipped; otherwise it is discarded. Must be called with mu held.
func (g *Game) tryStealItem(winner, loser *Player) string {
	if mathrand.Intn(100) >= 3 {
		return ""
	}
	candidates := make([]int, 0, 10)
	for i, v := range loser.Items {
		if v > 0 {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return ""
	}
	slot := candidates[mathrand.Intn(len(candidates))]
	stolen := loser.Items[slot]
	stolenName := loser.ItemNames[slot]
	loser.Items[slot] = 0
	loser.ItemNames[slot] = ""
	itemDesc := itemSlots[slot]
	if stolenName != "" {
		itemDesc = stolenName + " (" + itemSlots[slot] + ")"
	}
	if stolen > winner.Items[slot] {
		winner.Items[slot] = stolen
		winner.ItemNames[slot] = stolenName
		return fmt.Sprintf("%s steals %s's %s (level %d) and equips it!",
			winner.Nick, loser.Nick, itemDesc, stolen)
	}
	return fmt.Sprintf("%s steals %s's %s (level %d) but it's worse than their own — discarded.",
		winner.Nick, loser.Nick, itemDesc, stolen)
}

// randomEvent fires one of five equally likely individual events for p:
// TTL calamity, TTL godsend, item calamity, item godsend, or found item.
// The magnitude is 5–12% for all TTL and item changes.
// Must be called with mu held.
func (g *Game) randomEvent(p *Player) string {
	pct := mathrand.Intn(8) + 5
	change := p.TTL * int64(pct) / 100
	if change < 1 {
		change = 1
	}

	switch mathrand.Intn(5) {
	case 0: // TTL calamity
		p.TTL += change
		return fmt.Sprintf(calamityMsgs[mathrand.Intn(len(calamityMsgs))], p.Nick, pct)

	case 1: // TTL godsend
		p.TTL -= change
		if p.TTL < 1 {
			p.TTL = 1
		}
		return fmt.Sprintf(godsendMsgs[mathrand.Intn(len(godsendMsgs))], p.Nick, pct)

	case 2: // Item calamity — degrade one non-zero slot
		slot := g.pickNonZeroSlot(p)
		if slot < 0 {
			// No items yet; fall back to a TTL calamity.
			p.TTL += change
			return fmt.Sprintf(calamityMsgs[0], p.Nick, pct)
		}
		old := p.Items[slot]
		p.Items[slot] = int(math.Max(float64(old)*float64(100-pct)/100, 1))
		return fmt.Sprintf(itemCalamityMsgs[mathrand.Intn(len(itemCalamityMsgs))], p.Nick, itemSlots[slot], pct)

	case 3: // Item godsend — improve one slot (creates a level-1 item if all empty)
		slot := g.pickNonZeroSlot(p)
		if slot < 0 {
			slot = mathrand.Intn(10)
			p.Items[slot] = 1
		}
		old := p.Items[slot]
		p.Items[slot] = int(math.Max(float64(old)*float64(100+pct)/100, float64(old)+1))
		return fmt.Sprintf(itemGodsendMsgs[mathrand.Intn(len(itemGodsendMsgs))], p.Nick, itemSlots[slot], pct)

	default: // Found item — random slot, level up to 1.5× player level
		slot := mathrand.Intn(10)
		maxItem := int(math.Max(float64(p.Level)*1.5, 1))
		found := mathrand.Intn(maxItem) + 1
		equipped := "but it's worse than their current one"
		if found > p.Items[slot] {
			p.Items[slot] = found
			equipped = "and equips it"
		}
		return fmt.Sprintf("%s stumbles upon a %s of level %d on the road %s! [item total: %d]",
			p.Nick, itemSlots[slot], found, equipped, p.itemSum())
	}
}

// pickNonZeroSlot returns the index of a randomly chosen item slot that
// currently has a value > 0, or -1 if all slots are empty.
// Must be called with mu held.
func (g *Game) pickNonZeroSlot(p *Player) int {
	candidates := make([]int, 0, 10)
	for i, v := range p.Items {
		if v > 0 {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return -1
	}
	return candidates[mathrand.Intn(len(candidates))]
}

// handOfGod fires a dramatic divine intervention on a random online player.
// 80% chance to help (5–75% TTL reduction), 20% chance to hurt (same range).
// Must be called with mu held.
func (g *Game) handOfGod(p *Player) string {
	pct := mathrand.Intn(71) + 5 // 5–75%
	change := p.TTL * int64(pct) / 100
	if change < 1 {
		change = 1
	}
	if mathrand.Intn(5) == 0 { // 20% hurt
		p.TTL += change
		return fmt.Sprintf(handOfGodMsgs[0][mathrand.Intn(len(handOfGodMsgs[0]))], p.Nick, pct)
	}
	p.TTL -= change
	if p.TTL < 1 {
		p.TTL = 1
	}
	return fmt.Sprintf(handOfGodMsgs[1][mathrand.Intn(len(handOfGodMsgs[1]))], p.Nick, pct)
}

// teamBattle selects two random teams of three from the online players and
// runs a group fight. The winning team's TTL drops by 20% of their weakest
// member's TTL; the losing team's TTL increases by the same amount.
// Must be called with mu held.
func (g *Game) teamBattle(online []*Player) []string {
	shuffled := make([]*Player, len(online))
	copy(shuffled, online)
	mathrand.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	teamA := shuffled[:3]
	teamB := shuffled[3:6]

	sumA, sumB := 0, 0
	for _, p := range teamA {
		sumA += effectiveItemSum(p)
	}
	for _, p := range teamB {
		sumB += effectiveItemSum(p)
	}
	if sumA < 1 {
		sumA = 1
	}
	if sumB < 1 {
		sumB = 1
	}

	rollA := mathrand.Intn(sumA)
	rollB := mathrand.Intn(sumB)

	winners, losers := teamA, teamB
	wRoll, lRoll, wSum, lSum := rollA, rollB, sumA, sumB
	if rollB > rollA {
		winners, losers = teamB, teamA
		wRoll, lRoll, wSum, lSum = rollB, rollA, sumB, sumA
	}

	// Scale TTL change to the weakest winner so no single player is wiped out.
	minWinnerTTL := winners[0].TTL
	for _, p := range winners[1:] {
		if p.TTL < minWinnerTTL {
			minWinnerTTL = p.TTL
		}
	}
	change := minWinnerTTL * 20 / 100

	for _, p := range winners {
		p.TTL -= change
		if p.TTL < 0 {
			p.TTL = 0
		}
	}
	for _, p := range losers {
		p.TTL += change
	}

	names := func(team []*Player) string {
		ns := make([]string, len(team))
		for i, p := range team {
			ns[i] = p.Nick
		}
		return strings.Join(ns, ", ")
	}

	return []string{
		fmt.Sprintf("Team battle! [%s] (%d) vs [%s] (%d) — rolls %d vs %d.",
			names(winners), wSum, names(losers), lSum, wRoll, lRoll),
		fmt.Sprintf("[%s] win! Each winner's TTL drops by 20%% of their weakest member's TTL.", names(winners)),
	}
}

// tryStartQuest attempts to launch a quest when conditions are met: at least
// questMinPlayers players at questMinLevel+ are online. Randomly chooses
// between a grid quest (reach coordinates) and a time quest (stay online).
// Must be called with mu held.
func (g *Game) tryStartQuest(online []*Player) []string {
	eligible := make([]*Player, 0)
	for _, p := range online {
		if p.Level >= questMinLevel {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) < questMinPlayers {
		return nil
	}

	mathrand.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })
	questers := eligible[:questMinPlayers]

	desc := questDescs[mathrand.Intn(len(questDescs))]
	duration := time.Duration(mathrand.Intn(3)+1) * time.Hour // 1–3 hours

	names := make([]string, questMinPlayers)
	for i, p := range questers {
		names[i] = p.Nick
	}

	// Record who is online now; only these players will be penalised if the
	// quest fails (late-joiners are excluded from the penalty).
	onlineAtStart := make(map[string]bool, len(online))
	for _, p := range online {
		onlineAtStart[strings.ToLower(p.Nick)] = true
	}

	if mathrand.Intn(2) == 0 {
		qx := mathrand.Intn(gridSize)
		qy := mathrand.Intn(gridSize)
		g.quest = &Quest{
			Questers:      questers,
			EndsAt:        time.Now().Add(duration),
			Desc:          desc,
			OnlineAtStart: onlineAtStart,
			IsGrid:        true,
			QX:            qx,
			QY:            qy,
			Reached:       make(map[string]bool),
		}
		return []string{
			fmt.Sprintf("Grid quest begun! %s must navigate to (%d,%d) to %s. They have %s.",
				strings.Join(names, ", "), qx, qy, desc, fmtDuration(int64(duration.Seconds()))),
		}
	}

	g.quest = &Quest{
		Questers:      questers,
		EndsAt:        time.Now().Add(duration),
		Desc:          desc,
		OnlineAtStart: onlineAtStart,
	}
	return []string{
		fmt.Sprintf("Quest begun! %s have been sent to %s. They must complete it within %s.",
			strings.Join(names, ", "), desc, fmtDuration(int64(duration.Seconds()))),
	}
}

// resolveQuest determines success or failure for the active quest. Success
// requires all questers to still be online (and, for grid quests, having
// reached the target — that is handled by tickQuestProgress before this is
// called on timeout). On failure, only players who were online when the quest
// started receive the p15 penalty. Must be called with mu held.
func (g *Game) resolveQuest(online []*Player) []string {
	quest := g.quest

	onlineSet := make(map[*Player]bool, len(online))
	for _, p := range online {
		onlineSet[p] = true
	}
	allOnline := true
	for _, qp := range quest.Questers {
		if !onlineSet[qp] {
			allOnline = false
			break
		}
	}

	names := make([]string, len(quest.Questers))
	for i, p := range quest.Questers {
		names[i] = p.Nick
	}

	if allOnline {
		for _, qp := range quest.Questers {
			change := qp.TTL * 25 / 100
			qp.TTL -= change
			if qp.TTL < 1 {
				qp.TTL = 1
			}
		}
		if quest.IsGrid {
			return []string{
				fmt.Sprintf("Grid quest complete! %s have all reached (%d,%d) and succeeded in their quest to %s! Each receives a 25%% TTL bonus.",
					strings.Join(names, ", "), quest.QX, quest.QY, quest.Desc),
			}
		}
		return []string{
			fmt.Sprintf("Quest complete! %s have succeeded in their quest to %s! Each receives a 25%% TTL bonus.",
				strings.Join(names, ", "), quest.Desc),
		}
	}

	for _, p := range online {
		if quest.OnlineAtStart[strings.ToLower(p.Nick)] {
			g.applyPenalty(p, 15)
		}
	}
	if quest.IsGrid {
		reached := make([]string, 0, len(quest.Reached))
		for nick := range quest.Reached {
			reached = append(reached, nick)
		}
		suffix := "none reached the destination"
		if len(reached) > 0 {
			suffix = fmt.Sprintf("only %s reached (%d,%d)", strings.Join(reached, ", "), quest.QX, quest.QY)
		}
		return []string{
			fmt.Sprintf("Grid quest failed! %s did not all reach (%d,%d) to %s (%s). All online players suffer a penalty!",
				strings.Join(names, ", "), quest.QX, quest.QY, quest.Desc, suffix),
		}
	}
	return []string{
		fmt.Sprintf("Quest failed! %s did not complete their quest to %s in time. All online players suffer a penalty!",
			strings.Join(names, ", "), quest.Desc),
	}
}

// goodAlignmentEvent pairs p with a random good-aligned online partner and
// grants both a mutual 5–12% TTL reduction. Returns "" if no eligible partner
// is online. Must be called with mu held.
func (g *Game) goodAlignmentEvent(p *Player, online []*Player) string {
	var partners []*Player
	for _, op := range online {
		if op != p && op.Alignment == AlignGood {
			partners = append(partners, op)
		}
	}
	if len(partners) == 0 {
		return ""
	}
	partner := partners[mathrand.Intn(len(partners))]
	pct := mathrand.Intn(8) + 5
	for _, target := range []*Player{p, partner} {
		change := target.TTL * int64(pct) / 100
		if change < 1 {
			change = 1
		}
		target.TTL -= change
		if target.TTL < 1 {
			target.TTL = 1
		}
	}
	return fmt.Sprintf(goodEventMsgs[mathrand.Intn(len(goodEventMsgs))], p.Nick, partner.Nick, pct)
}

// evilAlignmentEvent either steals one item from a good-aligned player (50%
// chance when a target is available) or inflicts a forsaken penalty on p.
// Must be called with mu held.
func (g *Game) evilAlignmentEvent(p *Player, online []*Player) string {
	var goodTargets []*Player
	for _, op := range online {
		if op != p && op.Alignment == AlignGood {
			goodTargets = append(goodTargets, op)
		}
	}

	if len(goodTargets) > 0 && mathrand.Intn(2) == 0 {
		target := goodTargets[mathrand.Intn(len(goodTargets))]
		slot := g.pickNonZeroSlot(target)
		if slot >= 0 {
			stolen := target.Items[slot]
			target.Items[slot] = 0
			if stolen > p.Items[slot] {
				p.Items[slot] = stolen
			}
			return fmt.Sprintf(evilStealMsgs[mathrand.Intn(len(evilStealMsgs))],
				p.Nick, target.Nick, itemSlots[slot], stolen)
		}
	}

	// Forsaken: dark patron punishes the evil player.
	pct := mathrand.Intn(5) + 1
	change := p.TTL * int64(pct) / 100
	if change < 1 {
		change = 1
	}
	p.TTL += change
	return fmt.Sprintf(forsakenMsgs[mathrand.Intn(len(forsakenMsgs))], p.Nick, pct)
}

// applyPenalty adds base × 1.14^level seconds to p's TTL. The exponential
// factor means penalties grow with level, keeping them meaningful at high levels
// without being crippling for new players. Must be called with mu held.
func (g *Game) applyPenalty(p *Player, base int64) {
	p.TTL += int64(float64(base) * math.Pow(1.14, float64(p.Level)))
}

// findByAddr returns the online player whose stored Addr matches addr
// (case-insensitive). Returns nil if no online player matches.
// Must be called with mu held.
func (g *Game) findByAddr(addr string) *Player {
	lo := strings.ToLower(addr)
	for _, p := range g.players {
		if p.Online && strings.ToLower(p.Addr) == lo {
			return p
		}
	}
	return nil
}

// save marshals the player map to JSON and writes it atomically. Called after
// every state mutation so a crash never leaves the save file half-written.
func (g *Game) save() {
	if g.dataFile == "" {
		return
	}
	g.mu.Lock()
	data, err := json.MarshalIndent(g.players, "", "  ")
	g.mu.Unlock()
	if err != nil {
		log.Println("save error:", err)
		return
	}
	if err := writeFileAtomic(g.dataFile, data); err != nil {
		log.Println("write error:", err)
	}
}

// writeFileAtomic writes data to path via a sibling temp file followed by an
// os.Rename, which is atomic on Linux. Mode 0600 restricts read access to the
// owner, protecting the password hashes stored in the player file.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".save-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, path)
}

// load reads the player JSON file from disk. All players are marked offline
// after load; they re-authenticate via OnJoin or !login.
func (g *Game) load() {
	if g.dataFile == "" {
		return
	}
	data, err := os.ReadFile(g.dataFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Println("load error:", err)
		}
		return
	}
	if err := json.Unmarshal(data, &g.players); err != nil {
		log.Println("parse error:", err)
		return
	}
	for _, p := range g.players {
		p.Online = false
		p.Addr = ""
	}
	log.Printf("loaded %d players", len(g.players))
}

// ttlForLevel returns the number of seconds required to advance from level to
// level+1. The curve is:
//
//	levels 1–60:  600 × 1.16^level  seconds
//	levels 60+:   base_60 + 86400 × (level − 60)  seconds
//
// Adding one day per level beyond 60 prevents the exponential from becoming
// astronomically large while still rewarding dedicated long-term players.
// In DevMode all values are divided by 5 for faster testing.
func (g *Game) ttlForLevel(level int) int64 {
	var t int64
	if level <= 60 {
		t = int64(600 * math.Pow(1.16, float64(level)))
	} else {
		base := int64(600 * math.Pow(1.16, 60))
		t = base + int64(86400*(level-60))
	}
	if g.DevMode {
		t /= 5
	}
	return t
}

// newSalt generates a 16-byte cryptographically random salt and returns it
// as a 32-character lowercase hex string.
func newSalt() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// hashPass returns the SHA-256 hex digest of salt+pass. The salt is prepended
// in plain text so each player's hash is unique even when passwords match.
func hashPass(salt, pass string) string {
	h := sha256.Sum256([]byte(salt + pass))
	return fmt.Sprintf("%x", h)
}

// extractNick parses the nick out of a full IRC source string ("nick!user@host").
// Returns the full string unchanged if it contains no "!" separator.
func extractNick(src string) string {
	if idx := strings.Index(src, "!"); idx > 0 {
		return src[:idx]
	}
	return src
}

// idleFlavors are short strings appended to the channel topic when no players
// are registered or when everyone is offline and there is no recent event.
var idleFlavors = []string{
	"The realm awaits brave heroes.",
	"Silence fills the land — idle and grow strong.",
	"Fortune favours the patient.",
	"The gods grow restless for new champions.",
	"Adventure calls... but patience pays.",
	"Even legends began by doing nothing.",
}

// updateTopic rebuilds and sets the channel topic from current game state.
// Must NOT be called while holding mu.
func (g *Game) updateTopic() {
	if g.setTopic == nil {
		return
	}
	g.mu.Lock()
	topic := g.buildTopic()
	g.mu.Unlock()
	g.setTopic(topic)
}

// buildTopic assembles the full channel topic string from the current game
// state snapshot. Must be called with mu held.
func (g *Game) buildTopic() string {
	online, total, top := 0, len(g.players), (*Player)(nil)
	for _, p := range g.players {
		if p.Online {
			online++
		}
		if top == nil || p.Level > top.Level || (p.Level == top.Level && p.TTL < top.TTL) {
			top = p
		}
	}

	parts := []string{"⚔ IdleRPG"}
	if online == 0 && total == 0 {
		return strings.Join(append(parts, idleFlavors[mathrand.Intn(len(idleFlavors))]), " | ")
	}

	parts = append(parts, fmt.Sprintf("%d/%d idling", online, total))
	if top != nil {
		parts = append(parts, fmt.Sprintf("Top: %s lvl %d %s", top.Nick, top.Level, top.Class))
	}
	if qp := g.questTopicPart(); qp != "" {
		parts = append(parts, qp)
	}
	if g.lastEvent != "" {
		parts = append(parts, g.lastEvent)
	} else if online == 0 {
		parts = append(parts, idleFlavors[mathrand.Intn(len(idleFlavors))])
	}
	return strings.Join(parts, " | ")
}

// questTopicPart formats the active quest into a short topic segment.
// Returns "" when no quest is active. Must be called with mu held.
func (g *Game) questTopicPart() string {
	if g.quest == nil {
		return ""
	}
	remaining := fmtDuration(int64(time.Until(g.quest.EndsAt).Seconds()))
	if g.quest.IsGrid {
		return fmt.Sprintf("Grid quest: (%d,%d) — %s [%s left]",
			g.quest.QX, g.quest.QY, g.quest.Desc, remaining)
	}
	return fmt.Sprintf("Quest: %s [%s left]", g.quest.Desc, remaining)
}

// noteEvent records msg as the most recent notable event and refreshes the
// channel topic. Must NOT be called while holding mu.
func (g *Game) noteEvent(msg string) {
	g.mu.Lock()
	g.lastEvent = msg
	g.mu.Unlock()
	g.updateTopic()
}

// classFocusSlot maps a free-form class name to one of the ten item slot
// indices (0–9) using an FNV-1a hash. The mapping is deterministic and
// case-insensitive, so any two players with the same class share the same focus
// slot without requiring a fixed class registry.
func classFocusSlot(class string) int {
	h := uint32(2166136261) // FNV-1a offset basis
	for i := 0; i < len(class); i++ {
		c := class[i]
		if c >= 'A' && c <= 'Z' {
			c += 32 // fold to lowercase without importing unicode
		}
		h ^= uint32(c)
		h *= 16777619 // FNV prime
	}
	return int(h % 10)
}

// effectiveItemSum returns the battle-relevant item total for p. The raw
// itemSum is augmented by the focus-slot item level (counted an extra time)
// for each class. Dual-classed players add two bonuses; if both classes share
// the same focus slot the bonus stacks (that slot counts three times total).
func effectiveItemSum(p *Player) int {
	sum := p.itemSum() + p.Items[classFocusSlot(p.Class)]
	if p.Class2 != "" {
		sum += p.Items[classFocusSlot(p.Class2)]
	}
	return sum
}

// fmtDuration formats a duration given in seconds as a human-readable string
// in the form "Xh MM m SS s", "MM m SS s", or "SS s".
func fmtDuration(secs int64) string {
	if secs <= 0 {
		return "0s"
	}
	h := secs / 3600
	m := (secs % 3600) / 60
	s := secs % 60
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
