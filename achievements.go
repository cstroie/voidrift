// achievements.go defines the achievement/title system. Each achievement has
// an unlock condition evaluated against Player state. The highest-tier earned
// title is shown in !status; all earned/pending achievements in !achievements.
package main

import (
	"fmt"
	"strings"
)

// Achievement describes a single unlockable title. unlocked is a pure function
// of Player state; it is called under mu so must not block.
type Achievement struct {
	ID       string
	Title    string
	Desc     string
	Tier     int // higher = more prestigious; determines which title shows in !status
	unlocked func(*Player) bool
}

var allAchievements = []Achievement{
	// --- Level milestones ---
	{
		ID: "first_light", Title: "First Light", Tier: 5,
		Desc:     "Survived the initial void crossing (level 5).",
		unlocked: func(p *Player) bool { return p.Level >= 5 },
	},
	{
		ID: "signal_carrier", Title: "Signal Carrier", Tier: 15,
		Desc:     "Carried the signal further than most (level 15).",
		unlocked: func(p *Player) bool { return p.Level >= 15 },
	},
	{
		ID: "phase_walker", Title: "Phase Walker", Tier: 25,
		Desc:     "Learned to walk the phase line (level 25).",
		unlocked: func(p *Player) bool { return p.Level >= 25 },
	},
	{
		ID: "architects_pupil", Title: "Architect's Pupil", Tier: 35,
		Desc:     "Studied what the Architects left behind (level 35).",
		unlocked: func(p *Player) bool { return p.Level >= 35 },
	},
	{
		ID: "void_drifter_title", Title: "Void Drifter", Tier: 50,
		Desc:     "The void has become home (level 50).",
		unlocked: func(p *Player) bool { return p.Level >= 50 },
	},
	{
		ID: "echo_collapse", Title: "Echo of the Collapse", Tier: 75,
		Desc:     "Old enough to remember when everything fell (level 75).",
		unlocked: func(p *Player) bool { return p.Level >= 75 },
	},
	{
		ID: "null_sovereign", Title: "Null Sovereign", Tier: 100,
		Desc:     "Absolute mastery of nothing (level 100).",
		unlocked: func(p *Player) bool { return p.Level >= 100 },
	},
	// --- Battle ---
	{
		ID: "first_blood", Title: "Veteran", Tier: 3,
		Desc:     "Win your first battle.",
		unlocked: func(p *Player) bool { return p.BattlesWon >= 1 },
	},
	{
		ID: "duelist", Title: "Duelist", Tier: 9,
		Desc:     "Win 10 battles.",
		unlocked: func(p *Player) bool { return p.BattlesWon >= 10 },
	},
	{
		ID: "void_warrior", Title: "Void Warrior", Tier: 21,
		Desc:     "Win 50 battles.",
		unlocked: func(p *Player) bool { return p.BattlesWon >= 50 },
	},
	// --- Creeps ---
	{
		ID: "void_hunter", Title: "Void Hunter", Tier: 3,
		Desc:     "Defeat your first hostile creep.",
		unlocked: func(p *Player) bool { return p.CreepsSlain >= 1 },
	},
	{
		ID: "null_slayer", Title: "Null Slayer", Tier: 10,
		Desc:     "Defeat 10 hostile creeps.",
		unlocked: func(p *Player) bool { return p.CreepsSlain >= 10 },
	},
	{
		ID: "pale_killer", Title: "Pale Killer", Tier: 23,
		Desc:     "Defeat 50 hostile creeps.",
		unlocked: func(p *Player) bool { return p.CreepsSlain >= 50 },
	},
	// --- Quests ---
	{
		ID: "quester", Title: "Quester", Tier: 4,
		Desc:     "Complete your first quest.",
		unlocked: func(p *Player) bool { return p.QuestsCompleted >= 1 },
	},
	{
		ID: "quest_veteran", Title: "Quest Veteran", Tier: 14,
		Desc:     "Complete 5 quests.",
		unlocked: func(p *Player) bool { return p.QuestsCompleted >= 5 },
	},
	// --- Idle time ---
	{
		ID: "patient_one", Title: "Patient One", Tier: 4,
		Desc:     "Idle for a cumulative 24 hours.",
		unlocked: func(p *Player) bool { return p.TotalIdled >= 86400 },
	},
	{
		ID: "void_dweller", Title: "Void Dweller", Tier: 19,
		Desc:     "Idle for a cumulative 7 days.",
		unlocked: func(p *Player) bool { return p.TotalIdled >= 86400*7 },
	},
	// --- Items ---
	{
		ID: "relic_hunter", Title: "Relic Hunter", Tier: 6,
		Desc:     "Equip a Reclaimed-grade item.",
		unlocked: func(p *Player) bool { return hasRarityItem(p, rarityReclaimed) },
	},
	{
		ID: "architects_chosen", Title: "Architect's Chosen", Tier: 24,
		Desc:     "Equip an Architect-grade item.",
		unlocked: func(p *Player) bool { return hasRarityItem(p, rarityArchitect) },
	},
	{
		ID: "void_eternal_item", Title: "Void-Eternal", Tier: 62,
		Desc:     "Equip a Void-eternal item.",
		unlocked: func(p *Player) bool { return hasRarityItem(p, rarityVoidEternal) },
	},
}

// hasRarityItem reports whether p owns any item generated at the given rarity.
// Detection uses prefix matching against the rarity's word list; this is
// reliable because generateItemName always starts names with a rarity prefix.
func hasRarityItem(p *Player, rarity string) bool {
	var prefixes []string
	switch rarity {
	case rarityReclaimed:
		prefixes = uncommonPrefixes
	case rarityArchitect:
		prefixes = rarePrefixes
	case rarityVoidEternal:
		prefixes = legendaryPrefixes
	default:
		return false
	}
	for _, name := range p.ItemNames {
		if name == "" {
			continue
		}
		for _, pfx := range prefixes {
			if strings.HasPrefix(name, pfx+" ") || name == pfx {
				return true
			}
		}
	}
	return false
}

// hasAchievement reports whether p has already earned the achievement with id.
func hasAchievement(p *Player, id string) bool {
	for _, a := range p.Achievements {
		if a == id {
			return true
		}
	}
	return false
}

// earnedTitle returns the title of the highest-tier achievement p has earned,
// or "" if p has no achievements yet.
func earnedTitle(p *Player) string {
	best := 0
	title := ""
	for _, id := range p.Achievements {
		for _, a := range allAchievements {
			if a.ID == id && a.Tier > best {
				best = a.Tier
				title = a.Title
			}
		}
	}
	return title
}

// seedAchievements silently records all currently-earned achievements for p
// without producing announcements. Call on login so that achievements earned
// before this field existed (or before this session) are not re-announced.
// Must be called with mu held.
func (g *Game) seedAchievements(p *Player) {
	for _, a := range allAchievements {
		if !hasAchievement(p, a.ID) && a.unlocked(p) {
			p.Achievements = append(p.Achievements, a.ID)
		}
	}
}

// checkAchievements tests every achievement against p's current state and
// records any newly unlocked ones. Returns one announcement string per new
// unlock. Must be called with mu held.
func (g *Game) checkAchievements(p *Player) []string {
	var msgs []string
	for _, a := range allAchievements {
		if hasAchievement(p, a.ID) || !a.unlocked(p) {
			continue
		}
		p.Achievements = append(p.Achievements, a.ID)
		msgs = append(msgs, fmt.Sprintf(
			iB+cLime+"✦"+iC+iB+" "+iB+cCyan+"%s"+iC+iB+" unlocked "+iB+"[%s]"+iB+" — "+iI+"%s"+iI,
			p.Name, a.Title, a.Desc))
	}
	return msgs
}

// nextThreshold returns the first value in thresholds that is above current,
// or -1 if current already meets or exceeds the highest threshold.
func nextThreshold(current int, thresholds []int) int {
	for _, t := range thresholds {
		if current < t {
			return t
		}
	}
	return -1
}

// CmdAchievements returns a 2-line summary of p's achievements and progress
// toward the next unlock in each category.
func (g *Game) CmdAchievements(src, targetNick string) []string {
	if targetNick == "" {
		targetNick = extractNick(src)
	}
	g.mu.Lock()
	p, ok := g.players[strings.ToLower(targetNick)]
	g.mu.Unlock()
	if !ok {
		return []string{fmt.Sprintf("No character found for %s.", targetNick)}
	}

	// Line 1: header + list of earned titles.
	earned := make([]string, 0, len(p.Achievements))
	for _, a := range allAchievements {
		if hasAchievement(p, a.ID) {
			earned = append(earned, iB+"["+a.Title+"]"+iB)
		}
	}
	header := fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" — achievements (%d/%d)",
		p.Name, len(p.Achievements), len(allAchievements))
	if len(earned) > 0 {
		header += ": " + strings.Join(earned, " ")
	} else {
		header += ": none yet."
	}

	// Line 2: progress toward next milestone in each category.
	lvlNext := nextThreshold(p.Level, []int{5, 15, 25, 35, 50, 75, 100})
	batNext := nextThreshold(p.BattlesWon, []int{1, 10, 50})
	creepNext := nextThreshold(p.CreepsSlain, []int{1, 10, 50})
	questNext := nextThreshold(p.QuestsCompleted, []int{1, 5})
	idleNextSec := nextThreshold(int(p.TotalIdled), []int{86400, 86400 * 7})

	progress := func(cur, next int, unit string) string {
		if next < 0 {
			return unit + " ✓"
		}
		return fmt.Sprintf("%s %d/%d", unit, cur, next)
	}
	idleProgress := func() string {
		if idleNextSec < 0 {
			return "idle ✓"
		}
		return "idle " + fmtDuration(p.TotalIdled) + "/" + fmtDuration(int64(idleNextSec))
	}

	prog := "Progress: " +
		progress(p.Level, lvlNext, "lvl") + " · " +
		progress(p.BattlesWon, batNext, "battles") + " · " +
		progress(p.CreepsSlain, creepNext, "creeps") + " · " +
		progress(p.QuestsCompleted, questNext, "quests") + " · " +
		idleProgress()

	return []string{header, prog}
}
