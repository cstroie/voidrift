package main

import (
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"os"
	"sort"
	"strings"
)

// Guild represents a player-created group.
type Guild struct {
	Name    string
	Leader  string   // lowercase nick
	Members []string // lowercase nicks
	Invites []string // pending invites (lowercase nicks)
}

// totalLevel returns the sum of levels for all guild members found in the player map.
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

// hasMember reports whether nick (lowercase) is a member.
func (guild *Guild) hasMember(nick string) bool {
	for _, m := range guild.Members {
		if m == nick {
			return true
		}
	}
	return false
}

// hasInvite reports whether nick (lowercase) has a pending invite.
func (guild *Guild) hasInvite(nick string) bool {
	for _, inv := range guild.Invites {
		if inv == nick {
			return true
		}
	}
	return false
}

// removeMember removes nick (lowercase) from Members. Does not save.
func (guild *Guild) removeMember(nick string) {
	out := guild.Members[:0]
	for _, m := range guild.Members {
		if m != nick {
			out = append(out, m)
		}
	}
	guild.Members = out
}

// removeInvite removes nick (lowercase) from Invites. Does not save.
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

// CmdGCreate creates a new guild. The caller becomes the sole member and leader.
func (g *Game) CmdGCreate(src, name string) string {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 32 {
		return "Usage: !gcreate <name> (max 32 characters)"
	}
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
	// Founding a guild costs time.
	g.applyPenalty(p, 100)
	displayName := p.Nick
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf("%s has founded the guild %q! Use !ginvite <nick> to recruit members.", displayName, name)
}

// CmdGInvite invites a registered player to the caller's guild.
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
		return fmt.Sprintf("%s is already in a guild.", target.Nick)
	}
	if guild.hasInvite(tKey) {
		g.mu.Unlock()
		return fmt.Sprintf("%s already has a pending invite to %q.", target.Nick, guild.Name)
	}
	guild.Invites = append(guild.Invites, tKey)
	guildName := guild.Name
	inviterNick := p.Nick
	targetDisplayNick := target.Nick
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf("%s has been invited to %q by %s. They can type !gaccept to join.", targetDisplayNick, guildName, inviterNick)
}

// CmdGAccept accepts a pending guild invitation.
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
	// Find the guild that invited this player.
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
	displayNick := p.Nick
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf("%s has joined the guild %q!", displayNick, guildName)
}

// CmdGDecline declines a pending guild invitation.
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
	displayNick := p.Nick
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf("%s has declined the invitation to %q.", displayNick, guildName)
}

// CmdGLeave removes the caller from their guild. Disbands if last member; transfers
// leadership automatically if the caller is the leader.
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
	guildKey := strings.ToLower(guildName)
	displayNick := p.Nick

	guild.removeMember(nick)

	var msg string
	if len(guild.Members) == 0 {
		delete(g.guilds, guildKey)
		msg = fmt.Sprintf("%s has left %q — the guild is now disbanded.", displayNick, guildName)
	} else {
		if guild.Leader == nick {
			guild.Leader = guild.Members[0]
			msg = fmt.Sprintf("%s has left %q. Leadership passes to %s.", displayNick, guildName, guild.Leader)
		} else {
			msg = fmt.Sprintf("%s has left %q.", displayNick, guildName)
		}
	}
	g.mu.Unlock()
	g.saveGuilds()
	return msg
}

// CmdGKick removes a member from the guild. Only the leader can kick.
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
	tKey := strings.ToLower(targetNick)
	if tKey == leaderKey {
		g.mu.Unlock()
		return "You cannot kick yourself. Use !gleave to leave."
	}
	if !guild.hasMember(tKey) {
		g.mu.Unlock()
		return fmt.Sprintf("%s is not a member of %q.", targetNick, guild.Name)
	}
	guild.removeMember(tKey)
	guildName := guild.Name
	g.mu.Unlock()
	g.saveGuilds()
	return fmt.Sprintf("%s has been kicked from %q.", targetNick, guildName)
}

// CmdGInfo shows details about a guild. If name is empty, shows the caller's guild.
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
			marker = "*"
		}
		if p.Online {
			online++
		}
		memberInfo = append(memberInfo, fmt.Sprintf("%s%s(lvl %d)", marker, p.Nick, p.Level))
	}
	return fmt.Sprintf("[%s] Leader: %s | Members (%d online/%d): %s | Total level: %d",
		guild.Name, guild.Leader, online, len(guild.Members),
		strings.Join(memberInfo, ", "), total)
}

// CmdGTop returns the top 5 guilds by total member level.
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
		parts[i] = fmt.Sprintf("%d. %s (total lvl %d)", i+1, entries[i].name, entries[i].total)
	}
	return "Top guilds: " + strings.Join(parts, " | ")
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

// playerGuild returns the guild a nick (lowercase) belongs to, or nil. Must be called with mu held.
func (g *Game) playerGuild(nick string) *Guild {
	for _, guild := range g.guilds {
		if guild.hasMember(nick) {
			return guild
		}
	}
	return nil
}

// onlineGuildMembers returns online Player pointers for a guild. Must be called with mu held.
func (g *Game) onlineGuildMembers(guild *Guild) []*Player {
	out := make([]*Player, 0, len(guild.Members))
	for _, nick := range guild.Members {
		if p, ok := g.players[nick]; ok && p.Online {
			out = append(out, p)
		}
	}
	return out
}

// guildBattle picks two guilds with 2+ online members and pits them against each other.
// Must be called with mu held.
func (g *Game) guildBattle() []string {
	type candidate struct {
		guild   *Guild
		online  []*Player
		power   int
	}

	var candidates []candidate
	for _, guild := range g.guilds {
		online := g.onlineGuildMembers(guild)
		if len(online) < 2 {
			continue
		}
		power := 0
		for _, p := range online {
			power += p.itemSum()
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

	for _, p := range winners.online {
		change := p.TTL * 20 / 100
		p.TTL -= change
		if p.TTL < 1 {
			p.TTL = 1
		}
	}
	for _, p := range losers.online {
		change := p.TTL * 15 / 100
		p.TTL += change
	}

	winnerNames := make([]string, len(winners.online))
	for i, p := range winners.online {
		winnerNames[i] = p.Nick
	}
	loserNames := make([]string, len(losers.online))
	for i, p := range losers.online {
		loserNames[i] = p.Nick
	}

	return []string{
		fmt.Sprintf("Guild battle! [%s] (power %d, roll %d) vs [%s] (power %d, roll %d).",
			winners.guild.Name, winners.power, wRoll,
			losers.guild.Name, losers.power, lRoll),
		fmt.Sprintf("[%s] wins the guild battle! Members %s advance 20%%; members %s are set back 15%%.",
			winners.guild.Name,
			strings.Join(winnerNames, ", "),
			strings.Join(loserNames, ", ")),
	}
}

// ---------------------------------------------------------------------------
// Persistence
// ---------------------------------------------------------------------------

func (g *Game) saveGuilds() {
	if g.guildsFile == "" {
		return
	}
	g.mu.Lock()
	data, err := json.MarshalIndent(g.guilds, "", "  ")
	g.mu.Unlock()
	if err != nil {
		fmt.Println("saveGuilds error:", err)
		return
	}
	if err := os.WriteFile(g.guildsFile, data, 0644); err != nil {
		fmt.Println("writeGuilds error:", err)
	}
}

func (g *Game) loadGuilds() {
	if g.guildsFile == "" {
		return
	}
	data, err := os.ReadFile(g.guildsFile)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Println("loadGuilds error:", err)
		}
		return
	}
	if err := json.Unmarshal(data, &g.guilds); err != nil {
		fmt.Println("parseGuilds error:", err)
		return
	}
	fmt.Printf("loaded %d guilds\n", len(g.guilds))
}

