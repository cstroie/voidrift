// This file implements the guild system: data types, player-facing commands,
// guild battle logic, and JSON persistence.
//
// Guilds are stored in Game.guilds keyed by normalised lowercase name
// (whitespace collapsed). Member and leader fields use lowercase nicks to
// match the key used in Game.players.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	mathrand "math/rand"
	"os"
	"sort"
	"strings"
)

// guildBattleOpenMsgs announce a guild engagement.
// Args: winnerGuild, winnerPower, winnerRoll, loserGuild, loserPower, loserRoll.
var guildBattleOpenMsgs = []string{
	"Guild engagement! [" + iB + "%s" + iB + "] (force " + iB + cOrange + "%d" + iC + iB + ", roll " + iB + cOrange + "%d" + iC + iB + ") vs [" + iB + "%s" + iB + "] (force " + iB + cOrange + "%d" + iC + iB + ", roll " + iB + cOrange + "%d" + iC + iB + ").",
	"Faction conflict! [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ", roll " + iB + cOrange + "%d" + iC + iB + ") vs [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ", roll " + iB + cOrange + "%d" + iC + iB + ").",
	"Cohort battle: [" + iB + "%s" + iB + "] (force " + iB + cOrange + "%d" + iC + iB + "/roll " + iB + cOrange + "%d" + iC + iB + ") meets [" + iB + "%s" + iB + "] (force " + iB + cOrange + "%d" + iC + iB + "/roll " + iB + cOrange + "%d" + iC + iB + ").",
	"The void forces an accounting: [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ", " + iB + cOrange + "%d" + iC + iB + ") meets [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ", " + iB + cOrange + "%d" + iC + iB + ").",
}

// guildBattleWinMsgs announce the outcome. Args: winnerGuild, winnerMembers, loserMembers, winPct, losPct.
var guildBattleWinMsgs = []string{
	"[" + iB + cLime + "%s" + iC + iB + "] wins the engagement. " + iB + "%s" + iB + " advance phase by " + iB + cTeal + "%d%%" + iC + iB + "; " + iB + "%s" + iB + " are set back " + iB + cRed + "%d%%" + iC + iB + ".",
	"[" + iB + cLime + "%s" + iC + iB + "] breaks the opposing faction. " + iB + "%s" + iB + ": phase advanced by " + iB + cTeal + "%d%%" + iC + iB + ". " + iB + "%s" + iB + ": phase delayed by " + iB + cRed + "%d%%" + iC + iB + ".",
	"[" + iB + cLime + "%s" + iC + iB + "] holds the field. " + iB + "%s" + iB + " gain " + iB + cTeal + "%d%%" + iC + iB + " phase; " + iB + "%s" + iB + " lose " + iB + cRed + "%d%%" + iC + iB + ".",
	"[" + iB + cLime + "%s" + iC + iB + "] drives the rival cell back. " + iB + "%s" + iB + ": phase advanced by " + iB + cTeal + "%d%%" + iC + iB + ". " + iB + "%s" + iB + ": phase delayed by " + iB + cRed + "%d%%" + iC + iB + ".",
}

// Guild represents a player-created group. All string fields that hold nicks
// use lowercase nicks to match the keys in Game.players.
type Guild struct {
	Name    string   // display name, case-preserved
	Leader  string   // lowercase nick of the current leader
	Members []string // lowercase nicks of all members (including the leader)
	Invites []string // lowercase nicks with a pending invitation
}

// totalLevel returns the sum of levels for all guild members found in players.
// Missing players (e.g. deleted accounts) are silently skipped.
// Must be called with mu held.
func (guild *Guild) totalLevel(players map[string]*Player) int {
	total := 0
	for _, nick := range guild.Members {
		if p, ok := players[nick]; ok {
			total += p.Level
		}
	}
	return total
}

// hasMember reports whether nick (lowercase) is currently a member.
func (guild *Guild) hasMember(nick string) bool {
	for _, m := range guild.Members {
		if m == nick {
			return true
		}
	}
	return false
}

// hasInvite reports whether nick (lowercase) has a pending invitation.
func (guild *Guild) hasInvite(nick string) bool {
	for _, inv := range guild.Invites {
		if inv == nick {
			return true
		}
	}
	return false
}

// removeMember removes nick (lowercase) from Members in-place. Does not save.
func (guild *Guild) removeMember(nick string) {
	out := guild.Members[:0]
	for _, m := range guild.Members {
		if m != nick {
			out = append(out, m)
		}
	}
	guild.Members = out
}

// removeInvite removes nick (lowercase) from Invites in-place. Does not save.
func (guild *Guild) removeInvite(nick string) {
	out := guild.Invites[:0]
	for _, inv := range guild.Invites {
		if inv != nick {
			out = append(out, inv)
		}
	}
	guild.Invites = out
}

// ---------------------------------------------------------------------------
// Guild commands
// ---------------------------------------------------------------------------

// CmdGCreate creates a new guild with the caller as sole member and leader.
// The caller must be online, not already in a guild, and the name must be
// unique. Founding costs a p100 penalty. The guild name is stored as provided
// (after TrimSpace) but looked up case-insensitively with whitespace collapsed.
func (g *Game) CmdGCreate(src, name string) string {
	name = sanitize(name)
	if name == "" || len(name) > 32 {
		return "Usage: !gcreate <name> (max 32 characters)"
	}
	if !isValidName(name) {
		return "Guild name may only contain letters, digits, spaces, hyphens, apostrophes, and dots."
	}
	// Normalise: lowercase + collapse internal whitespace. This key is used
	// for all guild map lookups throughout the codebase.
	key := strings.ToLower(strings.Join(strings.Fields(name), " "))

	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	nick := strings.ToLower(p.Nick)
	if g.playerGuild(nick) != nil {
		g.mu.Unlock()
		return "You are already in a guild. Leave it first with !gleave."
	}
	if _, exists := g.guilds[key]; exists {
		g.mu.Unlock()
		return fmt.Sprintf("A guild named %q already exists.", name)
	}
	guild := &Guild{
		Name:    name,
		Leader:  nick,
		Members: []string{nick},
	}
	g.guilds[key] = guild
	g.applyPenalty(p, 100, penOther)
	displayName := p.Name
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has established the faction "+iB+"[%s]"+iB+". Use !ginvite <nick> to recruit members.", displayName, name)
}

// CmdGInvite invites a registered player to the caller's guild. Only the
// current leader may invite. The target must not already be in a guild and
// must not already have a pending invitation.
func (g *Game) CmdGInvite(src, targetNick string) string {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	leaderKey := strings.ToLower(p.Nick)
	guild := g.playerGuild(leaderKey)
	if guild == nil {
		g.mu.Unlock()
		return "You are not in a guild."
	}
	if guild.Leader != leaderKey {
		g.mu.Unlock()
		return "Only the guild leader can invite players."
	}
	tKey := strings.ToLower(targetNick)
	target, exists := g.players[tKey]
	if !exists {
		g.mu.Unlock()
		return fmt.Sprintf("No character registered as %s.", targetNick)
	}
	if g.playerGuild(tKey) != nil {
		g.mu.Unlock()
		return fmt.Sprintf("%s is already in a guild.", target.Name)
	}
	if guild.hasInvite(tKey) {
		g.mu.Unlock()
		return fmt.Sprintf("%s already has a pending invite to %q.", target.Name, guild.Name)
	}
	guild.Invites = append(guild.Invites, tKey)
	guildName := guild.Name
	inviterNick := p.Name
	targetDisplayNick := target.Name
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has been offered membership in "+iB+"[%s]"+iB+" by "+iB+cCyan+"%s"+iC+iB+". Type !gaccept to join.", targetDisplayNick, guildName, inviterNick)
}

// CmdGAccept accepts the first pending guild invitation found for the caller.
// The caller must not already be in a guild.
func (g *Game) CmdGAccept(src string) string {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	nick := strings.ToLower(p.Nick)
	if g.playerGuild(nick) != nil {
		g.mu.Unlock()
		return "You are already in a guild."
	}
	var invGuild *Guild
	for _, guild := range g.guilds {
		if guild.hasInvite(nick) {
			invGuild = guild
			break
		}
	}
	if invGuild == nil {
		g.mu.Unlock()
		return "You have no pending guild invitation."
	}
	invGuild.removeInvite(nick)
	invGuild.Members = append(invGuild.Members, nick)
	guildName := invGuild.Name
	displayNick := p.Name
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has integrated into faction "+iB+"[%s]"+iB+".", displayNick, guildName)
}

// CmdGDecline declines the first pending guild invitation found for the caller.
func (g *Game) CmdGDecline(src string) string {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	nick := strings.ToLower(p.Nick)
	var invGuild *Guild
	for _, guild := range g.guilds {
		if guild.hasInvite(nick) {
			invGuild = guild
			break
		}
	}
	if invGuild == nil {
		g.mu.Unlock()
		return "You have no pending guild invitation."
	}
	invGuild.removeInvite(nick)
	guildName := invGuild.Name
	displayNick := p.Name
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf("%s has declined the invitation to %q.", displayNick, guildName)
}

// CmdGLeave removes the caller from their guild. If they are the leader,
// leadership is transferred to the next member in the list. If they are the
// last member, the guild is disbanded entirely.
func (g *Game) CmdGLeave(src string) string {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	nick := strings.ToLower(p.Nick)
	guild := g.playerGuild(nick)
	if guild == nil {
		g.mu.Unlock()
		return "You are not in a guild."
	}
	guildName := guild.Name
	// Use the same key normalisation as CmdGCreate to ensure the delete hits
	// the correct map entry even when the name contains internal whitespace.
	guildKey := strings.ToLower(strings.Join(strings.Fields(guildName), " "))
	displayNick := p.Name

	guild.removeMember(nick)

	var msg string
	if len(guild.Members) == 0 {
		delete(g.guilds, guildKey)
		msg = fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has left "+iB+"[%s]"+iB+" — the faction is disbanded.", displayNick, guildName)
	} else {
		if guild.Leader == nick {
			guild.Leader = guild.Members[0]
			msg = fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has left "+iB+"[%s]"+iB+". Command transfers to "+iB+cCyan+"%s"+iC+iB+".", displayNick, guildName, guild.Leader)
		} else {
			msg = fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has left "+iB+"[%s]"+iB+".", displayNick, guildName)
		}
	}
	g.mu.Unlock()
	g.saveGuilds()
	return msg
}

// CmdGKick removes targetNick from the caller's guild. Only the leader may
// kick; the leader cannot kick themselves (use !gleave instead).
func (g *Game) CmdGKick(src, targetNick string) string {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	leaderKey := strings.ToLower(p.Nick)
	guild := g.playerGuild(leaderKey)
	if guild == nil {
		g.mu.Unlock()
		return "You are not in a guild."
	}
	if guild.Leader != leaderKey {
		g.mu.Unlock()
		return "Only the guild leader can kick members."
	}
	tKey := strings.ToLower(strings.TrimSpace(targetNick))
	if tKey == leaderKey {
		g.mu.Unlock()
		return "You cannot kick yourself. Use !gleave to leave."
	}
	if !guild.hasMember(tKey) {
		g.mu.Unlock()
		return "That player is not a member of your guild."
	}
	// Use the stored nick from the player record, not the raw input, to avoid
	// reflecting user-controlled strings back into channel messages.
	tp := g.players[tKey]
	storedNick := tKey
	if tp != nil {
		storedNick = tp.Name
	}
	guild.removeMember(tKey)
	guildName := guild.Name
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has been purged from "+iB+"[%s]"+iB+".", storedNick, guildName)
}

// CmdGInfo shows a summary of the requested guild: leader, member list with
// levels, online count, and total combined level. If name is empty, shows the
// caller's own guild.
func (g *Game) CmdGInfo(src, name string) string {
	g.mu.Lock()
	defer g.mu.Unlock()

	var guild *Guild
	if name == "" {
		p := g.findByAddr(src)
		if p == nil {
			return "You are not logged in. Specify a guild name: !ginfo <name>"
		}
		guild = g.playerGuild(strings.ToLower(p.Nick))
		if guild == nil {
			return "You are not in a guild. Specify a guild name: !ginfo <name>"
		}
	} else {
		guild = g.guilds[strings.ToLower(strings.Join(strings.Fields(name), " "))]
		if guild == nil {
			return fmt.Sprintf("No guild named %q.", name)
		}
	}

	total := guild.totalLevel(g.players)
	online := 0
	memberInfo := make([]string, 0, len(guild.Members))
	for _, nick := range guild.Members {
		p := g.players[nick]
		if p == nil {
			continue
		}
		marker := ""
		if nick == guild.Leader {
			marker = "⚑ "
		}
		if p.Online {
			online++
		}
		memberInfo = append(memberInfo, fmt.Sprintf("%s"+iB+cCyan+"%s"+iC+iB+" (lvl "+iB+"%d"+iB+")", marker, p.Name, p.Level))
	}
	return fmt.Sprintf(iB+"[%s]"+iB+" Leader: "+iB+cCyan+"%s"+iC+iB+" | Members ("+iB+"%d"+iB+" online/"+iB+"%d"+iB+"): %s | Total level: "+iB+"%d"+iB,
		guild.Name, guild.Leader, online, len(guild.Members),
		strings.Join(memberInfo, ", "), total)
}

// CmdGTop returns the top 5 guilds ranked by total member level.
func (g *Game) CmdGTop() string {
	g.mu.Lock()
	type entry struct {
		name  string
		total int
	}
	entries := make([]entry, 0, len(g.guilds))
	for _, guild := range g.guilds {
		entries = append(entries, entry{guild.Name, guild.totalLevel(g.players)})
	}
	g.mu.Unlock()

	if len(entries) == 0 {
		return "No guilds yet. Use !gcreate <name> to found one."
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].total > entries[j].total })
	n := 5
	if len(entries) < n {
		n = len(entries)
	}
	parts := make([]string, n)
	for i := 0; i < n; i++ {
		parts[i] = fmt.Sprintf("%d. "+iB+"%s"+iB+" (lvl "+iB+"%d"+iB+")", i+1, entries[i].name, entries[i].total)
	}
	return iB + "Top factions:" + iB + " " + strings.Join(parts, " | ")
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// playerGuild returns the guild that nick (lowercase) belongs to, or nil.
// Must be called with mu held.
func (g *Game) playerGuild(nick string) *Guild {
	for _, guild := range g.guilds {
		if guild.hasMember(nick) {
			return guild
		}
	}
	return nil
}

// onlineGuildMembers returns Player pointers for all online members of guild.
// Must be called with mu held.
func (g *Game) onlineGuildMembers(guild *Guild) []*Player {
	out := make([]*Player, 0, len(guild.Members))
	for _, nick := range guild.Members {
		if p, ok := g.players[nick]; ok && p.Online {
			out = append(out, p)
		}
	}
	return out
}

// guildBattle selects two guilds that each have at least two online members
// and runs a fight between them. Each guild's power is the sum of
// effectiveItemSum across its online members. Winners receive −20% TTL;
// losers receive +15% TTL. Returns nil if fewer than two eligible guilds exist.
// Must be called with mu held.
func (g *Game) guildBattle() []string {
	type candidate struct {
		guild  *Guild
		online []*Player
		power  int
	}

	var candidates []candidate
	for _, guild := range g.guilds {
		online := g.onlineGuildMembers(guild)
		if len(online) < 2 {
			continue
		}
		power := 0
		for _, p := range online {
			power += effectiveItemSum(p)
		}
		candidates = append(candidates, candidate{guild, online, power})
	}
	if len(candidates) < 2 {
		return nil
	}

	mathrand.Shuffle(len(candidates), func(i, j int) { candidates[i], candidates[j] = candidates[j], candidates[i] })
	ca, cb := candidates[0], candidates[1]

	if ca.power < 1 {
		ca.power = 1
	}
	if cb.power < 1 {
		cb.power = 1
	}

	rollA := mathrand.Intn(ca.power)
	rollB := mathrand.Intn(cb.power)

	winners, losers := ca, cb
	wRoll, lRoll := rollA, rollB
	if rollB > rollA {
		winners, losers = cb, ca
		wRoll, lRoll = rollB, rollA
	}

	winPct := int64(mathrand.Intn(14) + 12) // 12–25%
	losPct := int64(mathrand.Intn(11) + 5)  // 5–15%

	for _, p := range winners.online {
		change := p.TTL * winPct / 100
		p.TTL -= change
		if p.TTL < 1 {
			p.TTL = 1
		}
	}
	for _, p := range losers.online {
		change := p.TTL * losPct / 100
		p.TTL += change
	}

	winnerNames := make([]string, len(winners.online))
	for i, p := range winners.online {
		winnerNames[i] = p.Name
	}
	loserNames := make([]string, len(losers.online))
	for i, p := range losers.online {
		loserNames[i] = p.Name
	}

	return []string{
		eventHeader("☠️", "GUILD BATTLE"),
		fmt.Sprintf(guildBattleOpenMsgs[mathrand.Intn(len(guildBattleOpenMsgs))],
			winners.guild.Name, winners.power, wRoll,
			losers.guild.Name, losers.power, lRoll),
		fmt.Sprintf(guildBattleWinMsgs[mathrand.Intn(len(guildBattleWinMsgs))],
			winners.guild.Name,
			strings.Join(winnerNames, ", "),
			winPct,
			strings.Join(loserNames, ", "),
			losPct),
	}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

// saveGuilds marshals the guild map to JSON and writes it atomically.
func (g *Game) saveGuilds() {
	if g.guildsFile == "" {
		return
	}
	g.mu.Lock()
	data, err := json.MarshalIndent(g.guilds, "", "  ")
	g.mu.Unlock()
	if err != nil {
		log.Println("saveGuilds error:", err)
		return
	}
	if err := writeFileAtomic(g.guildsFile, data); err != nil {
		log.Println("writeGuilds error:", err)
	}
}

// loadGuilds reads guild data from disk. Missing files are silently ignored.
func (g *Game) loadGuilds() {
	if g.guildsFile == "" {
		return
	}
	data, err := os.ReadFile(g.guildsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Println("loadGuilds error:", err)
		}
		return
	}
	if err := json.Unmarshal(data, &g.guilds); err != nil {
		log.Println("parseGuilds error:", err)
		return
	}
	log.Printf("loaded %d guilds", len(g.guilds))
}
