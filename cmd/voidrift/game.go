// Package main implements VoidKeeper, the Void Drift IRC bot written in Go.
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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ircColorRe matches an IRC colour code: \x03 followed by an optional
// foreground number (1–2 digits) and an optional ,background number.
var ircColorRe = regexp.MustCompile(`\x03[0-9]{0,2}(?:,[0-9]{1,2})?`)

// ircControlReplacer strips the non-colour IRC formatting bytes.
var ircControlReplacer = strings.NewReplacer(
	"\x02", "", // bold
	"\x04", "", // hex colour (some clients)
	"\x0F", "", // reset
	"\x16", "", // reverse
	"\x1D", "", // italic
	"\x1E", "", // strikethrough
	"\x1F", "", // underline
	"\r", "", "\n", "", "\x00", "",
)

// stripIRC removes all IRC formatting codes from s, including colour codes
// and their trailing digit arguments, for clean plain-text log output.
func stripIRC(s string) string {
	s = ircColorRe.ReplaceAllString(s, "")
	return ircControlReplacer.Replace(s)
}

// sanitize strips IRC control codes and ASCII control characters from s and
// collapses runs of whitespace to a single space. Use for any string that will
// be echoed back into a channel message.
func sanitize(s string) string {
	s = stripIRC(s)
	// Strip remaining ASCII control chars (< 0x20, DEL).
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}

// isValidName reports whether s is acceptable as a class or guild name:
// Unicode letters and marks, digits, spaces, hyphens, apostrophes, and dots.
// This prevents IRC code injection while allowing names like "Void-Touched",
// "D'Ark", or "St. Elmo".
func isValidName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) {
			continue
		}
		switch r {
		case ' ', '-', '\'', '.':
			continue
		}
		return false
	}
	return true
}

const maxPassLen = 256 // bytes; prevents DoS via giant SHA-256 preimage

// itemSlots names the ten equipment slots in display order. The slice index is
// used everywhere items are stored (Player.Items, Player.ItemNames).
var itemSlots = [10]string{
	"implant", "beacon", "module", "weapon", "visor",
	"suit", "gauntlet", "hull plating", "deflector", "boot",
}

// IRC text-formatting constants for player-visible messages.
// Standard mIRC colour/bold/italic codes supported by virtually all clients.
const (
	iB = "\x02" // bold toggle
	iI = "\x1D" // italic toggle
	iC = "\x03" // end colour
	iR = "\x0F" // reset all

	cRed    = "\x0304" // red          – damage / bad events
	cTeal   = "\x0310" // teal         – phase gain / good events
	cCyan   = "\x0311" // light cyan   – player nicks
	cOrange = "\x0307" // orange       – battle rolls
	cPink   = "\x0313" // pink/magenta – Protocol ZERO
	cLime   = "\x0309" // lime green   – quest / team win

	fNick    = iB + cCyan + "%s" + iC + iB          // bold cyan nick
	fBadPct  = iB + cRed + "%d%%" + iC + iB         // bold red %
	fGoodPct = iB + cTeal + "%d%%" + iC + iB        // bold teal %
	fPct     = iB + "%d%%" + iB                     // bold neutral %
	fSlot    = iI + "%s" + iI                       // italic item label (unique name or slot name)
	fLvl     = iB + "%d" + iB                       // bold item level
	fRoll    = iB + cOrange + "[%d/%d]" + iC + iB   // bold orange roll
	fBot     = iB + cPink + "Protocol ZERO" + iC + iB
	fNullI   = iB + cPink + "Null-instance" + iC + iB
	fDesc    = iI + "%s" + iI // italic quest description
)

// Random-event message templates. Each uses fmt.Sprintf with (nick, pct) args.
var calamityMsgs = []string{
	fNick + "'s chrono-anchor destabilises in a cascade failure. Phase delayed by " + fBadPct + ".",
	"A tendril of the Drift brushes " + fNick + ". They lose time they cannot recover. Phase delayed by " + fBadPct + ".",
	"The Deep Signal bleeds into " + fNick + "'s neural feed. Phase delayed by " + fBadPct + ".",
	fNick + " is caught in a Null-tide. Forward momentum collapses. Phase delayed by " + fBadPct + ".",
	"Something beyond the Veil notices " + fNick + " — briefly. The attention costs them " + fBadPct + " phase.",
	"A dead star's echo reaches " + fNick + " at the worst moment. Phase delayed by " + fBadPct + ".",
	fNick + "'s phase-lock stutters. Lost in a loop they cannot name. Phase delayed by " + fBadPct + ".",
	"Entropic flux consumes " + fNick + "'s advancement window. Phase delayed by " + fBadPct + ".",
	"The Pale Architects mark " + fNick + " in passing. Their interest is not welcome. Phase delayed by " + fBadPct + ".",
	"A ghost-transmission from a fallen world drowns " + fNick + " in static. Phase delayed by " + fBadPct + ".",
	"The Null-tide rises and catches " + fNick + " mid-stride. Phase delayed by " + fBadPct + ".",
	"A resonance echo from the Collapse reverberates through " + fNick + "'s systems. Phase delayed by " + fBadPct + ".",
	fNick + " crosses a scar in space left by something that no longer exists. Phase delayed by " + fBadPct + ".",
	"The Veil thins near " + fNick + ". What looks back costs them " + fBadPct + " phase.",
	"Dead Architect code activates in " + fNick + "'s hardware uninvited. Phase delayed by " + fBadPct + ".",
	fNick + " is bitten by a creature that should not exist in this sector. Phase delayed by " + fBadPct + ".",
	fNick + " falls into a Drift-pocket and cannot find the exit. Phase delayed by " + fBadPct + ".",
	fNick + " accidentally broadcasts their position to something hungry. Phase delayed by " + fBadPct + ".",
	"A void-leech latches onto " + fNick + "'s power core and drains them. Phase delayed by " + fBadPct + ".",
	fNick + " inhales crystallised void-spores and spends " + fBadPct + " phase recovering.",
	"The Choir's harmonics overwhelm " + fNick + "'s cognition filters. Phase delayed by " + fBadPct + ".",
	fNick + " mistakes a Null-shard for a fuel cell. The error costs them " + fBadPct + " phase.",
	"A parasite from a dead world finds " + fNick + "'s neural stack hospitable. Phase delayed by " + fBadPct + ".",
	fNick + " triggers a pre-collapse alarm system. Running costs them " + fBadPct + " phase.",
	"Collapsed space-time briefly turns " + fNick + " inside out. Phase delayed by " + fBadPct + ".",
	fNick + " is struck by debris from a ship that exploded three centuries ago. Phase delayed by " + fBadPct + ".",
	"The Drift takes " + fNick + " apart and reassembles them slightly wrong. Phase delayed by " + fBadPct + ".",
	fNick + " stares into a Null-vortex. It stares back, and charges interest. Phase delayed by " + fBadPct + ".",
	"An automated Architect weapon system flags " + fNick + " as hostile. Evasion costs " + fBadPct + " phase.",
	fNick + " develops a compulsive loop in their decision matrix. Phase delayed by " + fBadPct + ".",
}

var godsendMsgs = []string{
	fNick + " intercepts a pre-collapse navigation burst. Phase advanced by " + fGoodPct + ".",
	"A fold in local spacetime carries " + fNick + " forward unexpectedly. Phase advanced by " + fGoodPct + ".",
	fNick + " decodes a shortcut buried in ancient Architect schematics. Phase advanced by " + fGoodPct + ".",
	"The Drift parts briefly around " + fNick + ". They move with sudden clarity. Phase advanced by " + fGoodPct + ".",
	fNick + " reads a ghost-transmission from a dead civilisation. The knowledge drives their phase forward by " + fGoodPct + ".",
	"A Null-eddy reverses around " + fNick + ", pushing them forward. Phase advanced by " + fGoodPct + ".",
	"The Signal stutters — " + fNick + " slips through the gap. Phase advanced by " + fGoodPct + ".",
	fNick + " finds a functioning relay beacon from before the Collapse. Phase advanced by " + fGoodPct + ".",
	"Residual energy from a Pale Architect transit carries " + fNick + " ahead. Phase advanced by " + fGoodPct + ".",
	fNick + " extracts a route-optimisation from a dead ship's black box. Phase advanced by " + fGoodPct + ".",
	"The void opens and closes in " + fNick + "'s favour for exactly three seconds. Phase advanced by " + fGoodPct + ".",
	"A surviving Architect sub-process identifies " + fNick + " as an asset and assists. Phase advanced by " + fGoodPct + ".",
	fNick + " threads a Drift pocket with unusual precision and emerges ahead. Phase advanced by " + fGoodPct + ".",
	"Something vast and cold passes near " + fNick + " — its wake accelerates them by " + fGoodPct + ".",
	"Coordinates from a destroyed vessel's last broadcast give " + fNick + " an edge. Phase advanced by " + fGoodPct + ".",
	fNick + " catches a unicorn-drone from a vanished bio-forge. Phase advanced by " + fGoodPct + ".",
	fNick + " discovers a secret passage carved through compressed spacetime. Phase advanced by " + fGoodPct + ".",
	"A tribe of void-adapted survivors teaches " + fNick + " their phase-compression technique. Phase advanced by " + fGoodPct + ".",
	fNick + " finds a one-use temporal accelerant and does not hesitate. Phase advanced by " + fGoodPct + ".",
	"An abandoned Architect sub-mind offers " + fNick + " a shortcut in exchange for nothing. Phase advanced by " + fGoodPct + ".",
	fNick + " tames a Drift-current and rides it forward. Phase advanced by " + fGoodPct + ".",
	"A radioactive anomaly grants " + fNick + " a sixth sense — briefly useful. Phase advanced by " + fGoodPct + ".",
	fNick + " barters passage through a Null-fold with a scavenger who asks no questions. Phase advanced by " + fGoodPct + ".",
	"The last automated act of a dead god-machine benefits " + fNick + " without explanation. Phase advanced by " + fGoodPct + ".",
	fNick + " upgrades their neural drive using schematics found floating in the Drift. Phase advanced by " + fGoodPct + ".",
	"A pre-collapse AI awakens for 4 seconds and optimises " + fNick + "'s trajectory. Phase advanced by " + fGoodPct + ".",
	fNick + " reverse-engineers a phase-loop and escapes three minutes early. Phase advanced by " + fGoodPct + ".",
	"The Drift forgets " + fNick + " exists for a moment. They use it well. Phase advanced by " + fGoodPct + ".",
	fNick + " decrypts a dead navigator's final log and finds the fast route. Phase advanced by " + fGoodPct + ".",
	"Something enormous and indifferent nudges " + fNick + " forward in passing. Phase advanced by " + fGoodPct + ".",
}

// Item-event templates. Args: (nick, itemLabel, pct).
// itemLabel is the unique item name if the slot has one, otherwise the slot name.
var itemCalamityMsgs = []string{
	fNick + "'s " + fSlot + " is corroded by entropic flux. Item degraded by " + fBadPct + ".",
	"A Null tendril phases through " + fNick + "'s " + fSlot + ", leaving it weakened. Item degraded by " + fBadPct + ".",
	fNick + "'s " + fSlot + " catastrophically vents during a void-wake collision. Item degraded by " + fBadPct + ".",
	"The Deep Signal resonates with " + fNick + "'s " + fSlot + " — badly. Item degraded by " + fBadPct + ".",
	"Drift exposure warps " + fNick + "'s " + fSlot + " beyond easy repair. Item degraded by " + fBadPct + ".",
	"A micro-collapse tears through " + fNick + "'s " + fSlot + ". Item degraded by " + fBadPct + ".",
	fNick + "'s " + fSlot + " takes a direct hit from a void-fragment. Item degraded by " + fBadPct + ".",
	"The Pale Architects' passing disrupts " + fNick + "'s " + fSlot + ". Item degraded by " + fBadPct + ".",
	"Unknown radiation from a dead star erodes " + fNick + "'s " + fSlot + ". Item degraded by " + fBadPct + ".",
	"Phase interference tears apart the lattice of " + fNick + "'s " + fSlot + ". Item degraded by " + fBadPct + ".",
	"A ghost-signal locks onto " + fNick + "'s " + fSlot + " and doesn't let go. Item degraded by " + fBadPct + ".",
	"Null-crystallisation spreads across " + fNick + "'s " + fSlot + " before halting. Item degraded by " + fBadPct + ".",
	"A void-parasite nests inside " + fNick + "'s " + fSlot + " and feeds. Item degraded by " + fBadPct + ".",
	fNick + "'s " + fSlot + " is partially dissolved by a Drift-acid pocket. Item degraded by " + fBadPct + ".",
	"Architect countermeasures mistake " + fNick + "'s " + fSlot + " for a threat and act accordingly. Item degraded by " + fBadPct + ".",
	"A temporal rift briefly ages " + fNick + "'s " + fSlot + " by decades. Item degraded by " + fBadPct + ".",
}

var itemDestroyedMsgs = []string{
	fNick + "'s " + fSlot + " crumbles to dust — nothing remains.",
	"The last fragment of " + fNick + "'s " + fSlot + " dissolves into the Void. It is gone.",
	"Entropic decay claims " + fNick + "'s " + fSlot + " entirely. Slot empty.",
	"A Null surge reduces " + fNick + "'s " + fSlot + " to scattered particles. Lost.",
	"The Drift finally consumes " + fNick + "'s " + fSlot + ". Nothing salvageable remains.",
	"Phase collapse annihilates " + fNick + "'s " + fSlot + " beyond any recovery.",
	"The Deep Signal unravels " + fNick + "'s " + fSlot + " at the molecular level. Gone.",
}

var itemGodsendMsgs = []string{
	fNick + " reverse-engineers Architect threading into their " + fSlot + ". Item improved by " + fGoodPct + ".",
	"A scavenger trades hard-won schematics — " + fNick + "'s " + fSlot + " is upgraded. Item improved by " + fGoodPct + ".",
	fNick + "'s " + fSlot + " absorbs resonant energy from a nearby collapse. Item improved by " + fGoodPct + ".",
	"Void exposure unexpectedly crystallises " + fNick + "'s " + fSlot + ". Item improved by " + fGoodPct + ".",
	fNick + " adapts pre-collapse alloys into their " + fSlot + ". Item improved by " + fGoodPct + ".",
	"A ghost-signal carries upgrade schematics for " + fNick + "'s " + fSlot + ". Item improved by " + fGoodPct + ".",
	"Phase-lock recalibration significantly improves " + fNick + "'s " + fSlot + ". Item improved by " + fGoodPct + ".",
	fNick + "'s " + fSlot + " bonds with residual Null-energy in an unexpected improvement. Item improved by " + fGoodPct + ".",
	"Drift-tempered metallurgy seeps into " + fNick + "'s " + fSlot + " — an accident that helps. Item improved by " + fGoodPct + ".",
	"An Architect micro-fabricator activates near " + fNick + " and reworks their " + fSlot + ". Item improved by " + fGoodPct + ".",
	"Void-annealing strengthens the core structure of " + fNick + "'s " + fSlot + ". Item improved by " + fGoodPct + ".",
	fNick + " patches their " + fSlot + " with salvaged Architect plating. It holds better than expected. Item improved by " + fGoodPct + ".",
	"A Drift-smith of unknown origin refines " + fNick + "'s " + fSlot + " without being asked. Item improved by " + fGoodPct + ".",
	fNick + " coats their " + fSlot + " in void-resin harvested from a collapsed star. Item improved by " + fGoodPct + ".",
	"Resonant feedback from a dead relay tower accidentally tempers " + fNick + "'s " + fSlot + ". Item improved by " + fGoodPct + ".",
	fNick + " trades a ghost-recording for Architect-grade components for their " + fSlot + ". Item improved by " + fGoodPct + ".",
}

// foundItemMsgs are used when a player stumbles upon a random item.
// Args: (playerName, article, itemName, itemLevel, equippedVerb, itemTotal).
var foundItemMsgs = []string{
	"%s stumbles upon %s %s of level %d in the wreckage of a pre-collapse freighter %s. [item total: %d]",
	"%s pulls %s %s of level %d from a derelict escape pod %s. [item total: %d]",
	"%s finds %s %s of level %d drifting in a debris field %s. [item total: %d]",
	"%s uncovers %s %s of level %d half-buried in solidified void-foam %s. [item total: %d]",
	"%s pries %s %s of level %d loose from a dead Architect construct %s. [item total: %d]",
	"%s intercepts a supply cache and claims %s %s of level %d from it %s. [item total: %d]",
	"%s extracts %s %s of level %d from a sealed Null-crate %s. [item total: %d]",
	"%s trades a ghost-signal recording for %s %s of level %d %s. [item total: %d]",
	"%s recovers %s %s of level %d from a pilot who no longer needs it %s. [item total: %d]",
}

// voidStormOpenMsgs: opening line of a void storm hitting all online players.
var voidStormOpenMsgs = []string{
	"A Null-tide sweeps the sector. Everyone caught in the open suffers.",
	"The Veil tears. A shockwave of collapsed spacetime rolls across the grid.",
	"Something vast dies beyond the observable threshold. Its death-pulse reaches everyone.",
	"The Deep Signal spikes to maximum and holds. All phase-locks destabilise.",
	"A Pale Architect construct detonates somewhere in the dark. The shockwave is not metaphorical.",
	"Void-foam crystallises across the sector without warning. Nobody is spared.",
	"The Drift inverts briefly. When it corrects, everyone has lost something.",
	"An entropy wave — origin unknown — propagates through the local grid.",
}

// voidStormHitMsgs: per-player effect line. Args: name, pct.
var voidStormHitMsgs = []string{
	fNick + " takes the full force. Phase delayed by " + fBadPct + ".",
	"The wave finds " + fNick + " exposed. Phase delayed by " + fBadPct + ".",
	fNick + " cannot outrun it. Phase delayed by " + fBadPct + ".",
	"The pulse tears through " + fNick + "'s systems. Phase delayed by " + fBadPct + ".",
}

// warpMsgs: player teleports to a new grid position. Args: name, x, y.
var warpMsgs = []string{
	fNick + " tears through a Drift-fold and surfaces at (" + iB + "%d,%d" + iB + ").",
	"A Null-current seizes " + fNick + " and deposits them at (" + iB + "%d,%d" + iB + ").",
	fNick + " slips through a collapsed Architect transit and emerges at (" + iB + "%d,%d" + iB + ").",
	"The void blinks. " + fNick + " is suddenly at (" + iB + "%d,%d" + iB + ").",
	fNick + " rides a ghost-signal spike across the grid, landing at (" + iB + "%d,%d" + iB + ").",
	"A phase-crack swallows " + fNick + " and spits them out at (" + iB + "%d,%d" + iB + ").",
	fNick + " steps into a Null-fold and steps out at (" + iB + "%d,%d" + iB + "). Physics optional.",
}

// levelUpItemMsgs are templates for announcing the item found on level-up.
// Each is a func(name, level, ttl, itemPhrase, equipped, label string) string,
// where itemPhrase is already fully formatted (e.g. "a lv3 suit" or
// "a lv40 Battered Plates"). They vary the narrative framing so the causality
// doesn't feel mechanical.
var levelUpItemMsgs = []func(name, level, ttl, itemPhrase, equipped, label string) string{
	// Perception: sharpened senses from the phase shift.
	func(name, level, ttl, itemPhrase, equipped, label string) string {
		return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has attained level "+iB+"%s"+iB+
			". Senses sharpened by the phase shift, they notice "+iI+"%s"+iI+
			" nearby%s%s. Next phase: "+iB+"%s"+iB+".",
			name, level, itemPhrase, equipped, label, ttl)
	},
	// Dislodged: phase surge shakes something loose.
	func(name, level, ttl, itemPhrase, equipped, label string) string {
		return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has attained level "+iB+"%s"+iB+
			". The phase surge dislodges "+iI+"%s"+iI+
			" from a nearby wreck%s%s. Next phase: "+iB+"%s"+iB+".",
			name, level, itemPhrase, equipped, label, ttl)
	},
	// Between cycles: scavenged in the drift, unrelated to leveling.
	func(name, level, ttl, itemPhrase, equipped, label string) string {
		return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has attained level "+iB+"%s"+iB+
			". Somewhere in the drift, between phase cycles, they recover "+iI+"%s"+iI+
			"%s%s. Next phase: "+iB+"%s"+iB+".",
			name, level, itemPhrase, equipped, label, ttl)
	},
	// Causality inverted: the salvage caused the breakthrough.
	func(name, level, ttl, itemPhrase, equipped, label string) string {
		return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has attained level "+iB+"%s"+iB+
			" after salvaging "+iI+"%s"+iI+
			"%s%s — it gave them the edge they needed. Next phase: "+iB+"%s"+iB+".",
			name, level, itemPhrase, equipped, label, ttl)
	},
	// Separate fact: item mentioned as an aside.
	func(name, level, ttl, itemPhrase, equipped, label string) string {
		return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has attained level "+iB+"%s"+iB+
			". Next phase: "+iB+"%s"+iB+
			". They've also been carrying "+iI+"%s"+iI+
			"%s%s.",
			name, level, ttl, itemPhrase, equipped, label)
	},
}

// handOfGodMsgs[0] = hurt templates, [1] = help templates. Args: (nick, pct).
var handOfGodMsgs = [2][]string{
	{
		"The Pale Architects turn their gaze on " + fNick + ". Their attention is not a gift. Phase delayed by " + fBadPct + ".",
		"Something reaches through the Veil and sets " + fNick + " back " + fBadPct + " phase. It does not explain itself.",
		"The Deep Signal locks onto " + fNick + ". They lose " + fBadPct + " phase fighting free of it.",
		"A Null-sovereign brushes past " + fNick + ". The encounter costs them " + fBadPct + " phase.",
		"The Drift takes an interest in " + fNick + ". By the time it loses interest, " + fBadPct + " phase is gone.",
		"The Entity known only as the Choir reaches through the static to adjust " + fNick + "'s trajectory. Phase delayed by " + fBadPct + ".",
		"A Pale Architect construct runs diagnostics on " + fNick + ". The process is invasive. Phase delayed by " + fBadPct + ".",
	},
	{
		"An Architect relay pulses near " + fNick + ". They ride the shockwave forward by " + fGoodPct + " phase.",
		"The Drift recedes from " + fNick + " without warning. They gain " + fGoodPct + " phase in the sudden clarity.",
		fNick + " intercepts a ghost-transmission from a dead god-machine. The knowledge is worth " + fGoodPct + " phase.",
		"Something vast and indifferent passes near " + fNick + " — they are briefly carried in its wake. Phase advanced by " + fGoodPct + ".",
		"A pre-collapse AI broadcasts a single optimisation burst. " + fNick + " catches it. Phase advanced by " + fGoodPct + ".",
		"The Signal aligns for " + fNick + " — an anomaly. Whatever caused it does not repeat. Phase advanced by " + fGoodPct + ".",
		"A dead Architect's final automated act benefits " + fNick + ". Phase advanced by " + fGoodPct + ". It will not happen again.",
	},
}

// promoMsgs are periodic self-promotion messages broadcast to the channel once
// per day. No format arguments — sent as-is.
var promoMsgs = []string{
	"⚡ " + iB + "Void Drift" + iB + " — an idle RPG for the end of the universe. Idle to level up. Source: \x02https://github.com/cstroie/voidrift\x02",
	"📡 The Drift never sleeps. Neither does " + iB + "Void Drift" + iB + ", the IRC idle RPG. Join, register, and let the void do the rest. \x02https://github.com/cstroie/voidrift\x02",
	"🌌 " + iB + "Void Drift" + iB + ": pick a class, pick a side, then do absolutely nothing. The universe handles the rest. \x02https://github.com/cstroie/voidrift\x02",
	"☠ The old gods are gone. What remains is " + iB + "Void Drift" + iB + " — an idle RPG played by surviving in a dying universe. \x02https://github.com/cstroie/voidrift\x02",
	"🔭 " + iB + "Void Drift" + iB + ": battles, quests, guilds, legendary artefacts — all while you do nothing. Open source Go IRC bot. \x02https://github.com/cstroie/voidrift\x02",
}

// battleMsgs: 1v1 announcements. Args: winner, wRoll, wSum, loser, lRoll, lSum, critNote, pct.
var battleMsgs = []string{
	fNick + " " + fRoll + " tears through " + fNick + " " + fRoll + "'s defences.%s Phase swing: " + fPct + ".",
	fNick + " " + fRoll + " overwhelms " + fNick + " " + fRoll + " in close contact.%s Phase shift: " + fPct + ".",
	fNick + " " + fRoll + " finds the gap in " + fNick + " " + fRoll + "'s pattern.%s Phase swing: " + fPct + ".",
	fNick + " " + fRoll + " outmanoeuvres " + fNick + " " + fRoll + " — brief and brutal.%s Phase swing: " + fPct + ".",
	fNick + " " + fRoll + " drives through " + fNick + " " + fRoll + "'s guard without slowing.%s Phase shift: " + fPct + ".",
	fNick + " " + fRoll + " strips " + fNick + " " + fRoll + "'s timing apart.%s Phase swing: " + fPct + ".",
	fNick + " " + fRoll + " outpaces " + fNick + " " + fRoll + " before the engagement starts.%s Phase swing: " + fPct + ".",
	fNick + " " + fRoll + " lands first and doesn't let " + fNick + " " + fRoll + " recover.%s Phase swing: " + fPct + ".",
	fNick + " " + fRoll + " locks " + fNick + " " + fRoll + " into a losing exchange.%s Phase shift: " + fPct + ".",
	fNick + " " + fRoll + " collapses " + fNick + " " + fRoll + "'s opening gambit and punishes it.%s Phase swing: " + fPct + ".",
}

// critNoteMsgs are inserted as %s into battleMsgs on a critical hit.
var critNoteMsgs = []string{
	" " + iB + cRed + "Phase-burst overload!" + iC + iB,
	" " + iB + cRed + "Null-resonance cascade!" + iC + iB,
	" " + iB + cRed + "Void-crack rupture!" + iC + iB,
	" " + iB + cRed + "Deep Signal surge!" + iC + iB,
	" " + iB + cRed + "Entropy spike!" + iC + iB,
	" " + iB + cRed + "Drift-fracture!" + iC + iB,
	" " + iB + cRed + "Pale Architect resonance!" + iC + iB,
}

// botBattle messages. Args: nick, pRoll, pSum, botRoll, botSum, pct.
var botBattleWinMsgs = []string{
	fNick + " " + fRoll + " punches through " + fBot + " " + fRoll + ". Phase advanced by " + fGoodPct + ".",
	fNick + " " + fRoll + " dismantles " + fBot + "'s defences " + fRoll + ". Phase advanced by " + fGoodPct + ".",
	fNick + " " + fRoll + " overwhelms the " + fNullI + " " + fRoll + " — for now. Phase advanced by " + fGoodPct + ".",
	fNick + " " + fRoll + " finds the crack in " + fBot + " " + fRoll + " and exploits it. Phase advanced by " + fGoodPct + ".",
	fNick + " " + fRoll + " outmanoeuvres " + fBot + " " + fRoll + " in a clean exchange. Phase advanced by " + fGoodPct + ".",
	fNick + " " + fRoll + " takes the " + fNullI + " " + fRoll + " apart without mercy. Phase advanced by " + fGoodPct + ".",
}

var botBattleLossMsgs = []string{
	fNick + " " + fRoll + " is repelled by " + fBot + " " + fRoll + ". Phase delayed by " + fBadPct + ".",
	fNick + " " + fRoll + " cannot breach the " + fNullI + " " + fRoll + ". Phase delayed by " + fBadPct + ".",
	fNick + " " + fRoll + " shatters against " + fBot + " " + fRoll + " and is thrown back. Phase delayed by " + fBadPct + ".",
	fNick + " " + fRoll + " exhausts every advantage against " + fBot + " " + fRoll + ". Phase delayed by " + fBadPct + ".",
	fNick + " " + fRoll + " finds no weakness in " + fBot + " " + fRoll + ". The retreat is costly. Phase delayed by " + fBadPct + ".",
	fNick + " " + fRoll + " is systematically dismantled by the " + fNullI + " " + fRoll + ". Phase delayed by " + fBadPct + ".",
}

// stealEquipMsgs and stealDiscardMsgs: post-battle item theft. Args: winner, loser, itemLabel, itemLevel.
// itemLabel is the unique item name if the slot has one, otherwise the slot name.
var stealEquipMsgs = []string{
	fNick + " strips " + fNick + "'s " + fSlot + " (lvl " + fLvl + ") and integrates it.",
	fNick + " extracts " + fNick + "'s " + fSlot + " (lvl " + fLvl + ") in the chaos and slots it in.",
	fNick + " tears " + fNick + "'s " + fSlot + " (lvl " + fLvl + ") free and makes it their own.",
	fNick + " exploits the opening to claim " + fNick + "'s " + fSlot + " (lvl " + fLvl + "). It fits.",
	fNick + " rips " + fNick + "'s " + fSlot + " (lvl " + fLvl + ") from the wreckage. Upgrade accepted.",
	fNick + " seizes " + fNick + "'s " + fSlot + " (lvl " + fLvl + ") before the dust settles. Better.",
}

var stealDiscardMsgs = []string{
	fNick + " strips " + fNick + "'s " + fSlot + " (lvl " + fLvl + ") — inferior to their own. Left in the void.",
	fNick + " takes " + fNick + "'s " + fSlot + " (lvl " + fLvl + ") but finds it lacking. Discarded.",
	fNick + " seizes " + fNick + "'s " + fSlot + " (lvl " + fLvl + "), examines it, drops it. Not worth the mass.",
	fNick + " checks " + fNick + "'s " + fSlot + " (lvl " + fLvl + "). Below spec. Abandoned.",
	fNick + " takes " + fNick + "'s " + fSlot + " (lvl " + fLvl + "), scans it, vents it into the void.",
}

// teamBattleOpenMsgs: team skirmish announcement. Args: winners, wSum, losers, lSum, wRoll, lRoll.
var teamBattleOpenMsgs = []string{
	"Skirmish! [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") clash with [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") — rolls " + iB + cOrange + "%d vs %d" + iC + iB + ".",
	"Team contact! [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") vs [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + "). Rolls: " + iB + cOrange + "%d vs %d" + iC + iB + ".",
	"Convergence: [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") and [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") meet in open space. Rolls: " + iB + cOrange + "%d vs %d" + iC + iB + ".",
	"Engagement logged: [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") vs [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + "). Rolls: " + iB + cOrange + "%d vs %d" + iC + iB + ".",
	"Formation clash! [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") drives into [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + "). Rolls: " + iB + cOrange + "%d vs %d" + iC + iB + ".",
	"Contact. [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") and [" + iB + "%s" + iB + "] (" + iB + cOrange + "%d" + iC + iB + ") — no avoiding it. Rolls: " + iB + cOrange + "%d vs %d" + iC + iB + ".",
}

// teamBattleWinMsgs: winning team announcement. Args: winners.
var teamBattleWinMsgs = []string{
	"[" + iB + cLime + "%s" + iC + iB + "] break through. Phase advanced by " + fGoodPct + " (weakest anchor).",
	"[" + iB + cLime + "%s" + iC + iB + "] hold the line and advance. Phase advanced by " + fGoodPct + " (weakest).",
	"[" + iB + cLime + "%s" + iC + iB + "] take the exchange — cleanly. Phase advanced by " + fGoodPct + " (weakest).",
	"[" + iB + cLime + "%s" + iC + iB + "] collapse the opposing formation. Phase advanced by " + fGoodPct + " (weakest anchor).",
	"[" + iB + cLime + "%s" + iC + iB + "] establish fire superiority and press it. Phase advanced by " + fGoodPct + ".",
	"[" + iB + cLime + "%s" + iC + iB + "] execute the engagement without error. Phase advanced by " + fGoodPct + " (weakest).",
}

// encounterMsgs: surprise grid collision. Args: nick1, nick2, x, y.
var encounterMsgs = []string{
	fNick + " and " + fNick + " cross paths at (" + iB + "%d,%d" + iB + ") — neither expected it.",
	fNick + " and " + fNick + " occupy the same scar in space at (" + iB + "%d,%d" + iB + ").",
	fNick + " and " + fNick + " collide at (" + iB + "%d,%d" + iB + "). The void watches.",
	"Proximity alert: " + fNick + " and " + fNick + " at (" + iB + "%d,%d" + iB + "). One of them will regret this.",
	fNick + " and " + fNick + " surface at the same coordinates (" + iB + "%d,%d" + iB + ").",
	fNick + " and " + fNick + " find themselves sharing the same dead zone at (" + iB + "%d,%d" + iB + ").",
	"The Drift deposits " + fNick + " and " + fNick + " at (" + iB + "%d,%d" + iB + "). Neither asked for this.",
	"Sensors confirm: " + fNick + " and " + fNick + " at (" + iB + "%d,%d" + iB + "). Resolution required.",
	fNick + " and " + fNick + " emerge from the static at (" + iB + "%d,%d" + iB + ") simultaneously.",
	"Something herds " + fNick + " and " + fNick + " together at (" + iB + "%d,%d" + iB + "). It watches.",
}

// tradeMsgs: two players meet and exchange gear. Args: nick1, nick2, x, y.
var tradeMsgs = []string{
	fNick + " and " + fNick + " meet at (" + iB + "%d,%d" + iB + ") and broker a deal in the drift.",
	fNick + " and " + fNick + " cross paths at (" + iB + "%d,%d" + iB + ") — salvage changes hands.",
	fNick + " and " + fNick + " haggle at (" + iB + "%d,%d" + iB + ") before drifting apart.",
	"A ghost-market deal between " + fNick + " and " + fNick + " at (" + iB + "%d,%d" + iB + ").",
	fNick + " and " + fNick + " trade cargo manifests at (" + iB + "%d,%d" + iB + ").",
	"The Drift pushes " + fNick + " and " + fNick + " together at (" + iB + "%d,%d" + iB + "). They make the most of it.",
}

// passByMsgs: two players drift past each other without incident. Args: nick1, nick2, x, y.
var passByMsgs = []string{
	fNick + " and " + fNick + " drift past each other at (" + iB + "%d,%d" + iB + ") without a word.",
	fNick + " and " + fNick + " share coordinates (" + iB + "%d,%d" + iB + ") briefly, then diverge.",
	fNick + " and " + fNick + " cross at (" + iB + "%d,%d" + iB + ") — a nod, nothing more.",
	"Proximity at (" + iB + "%d,%d" + iB + "): " + fNick + " and " + fNick + " keep their distance and move on.",
	fNick + " and " + fNick + " surface at (" + iB + "%d,%d" + iB + ") and go their separate ways.",
}

// guildMeetMsgs: two guild-mates rendezvous on the grid. Args: nick1, nick2, x, y.
var guildMeetMsgs = []string{
	fNick + " and " + fNick + " link up at (" + iB + "%d,%d" + iB + "). Guild colours recognised.",
	"Guild rendezvous: " + fNick + " and " + fNick + " converge at (" + iB + "%d,%d" + iB + ").",
	fNick + " spots " + fNick + " at (" + iB + "%d,%d" + iB + ") — friendly signal exchanged.",
	"The Drift brings " + fNick + " and " + fNick + " together at (" + iB + "%d,%d" + iB + "). At least these two trust each other.",
	fNick + " and " + fNick + " share the same void at (" + iB + "%d,%d" + iB + "). Allies, for now.",
}

// guildTradeMsgs: guild-mates exchange gear. Args: nick1, nick2, x, y.
var guildTradeMsgs = []string{
	fNick + " and " + fNick + " resupply each other at (" + iB + "%d,%d" + iB + "). The guild endures.",
	"Coordinated resupply: " + fNick + " and " + fNick + " swap loadouts at (" + iB + "%d,%d" + iB + ").",
	fNick + " and " + fNick + " pool salvage at (" + iB + "%d,%d" + iB + "). Better together.",
	"Guild logistics: " + fNick + " and " + fNick + " redistribute gear at (" + iB + "%d,%d" + iB + ").",
	fNick + " passes kit to " + fNick + " at (" + iB + "%d,%d" + iB + "). No credits change hands.",
}

// guildPassByMsgs: guild-mates check in and move on. Args: nick1, nick2, x, y.
var guildPassByMsgs = []string{
	fNick + " and " + fNick + " exchange status reports at (" + iB + "%d,%d" + iB + ") and split up.",
	"Brief debrief: " + fNick + " and " + fNick + " cross at (" + iB + "%d,%d" + iB + ") and continue on mission.",
	fNick + " and " + fNick + " acknowledge each other at (" + iB + "%d,%d" + iB + "). Drift resumes.",
	"Guild ping at (" + iB + "%d,%d" + iB + "): " + fNick + " confirms " + fNick + " is still in one piece.",
	fNick + " and " + fNick + " share coordinates (" + iB + "%d,%d" + iB + ") momentarily — then back to the void.",
}

// guildSpaMsgs: guild-mates spar — friendly contest. Args: nick1, nick2, x, y.
var guildSparMsgs = []string{
	fNick + " and " + fNick + " spar at (" + iB + "%d,%d" + iB + "). Friendly, mostly.",
	"Training drill: " + fNick + " and " + fNick + " trade blows at (" + iB + "%d,%d" + iB + ").",
	fNick + " and " + fNick + " test each other at (" + iB + "%d,%d" + iB + "). The guild grows stronger.",
	"Sparring session at (" + iB + "%d,%d" + iB + "): " + fNick + " vs " + fNick + ". All in good faith.",
	fNick + " challenges " + fNick + " to a quick bout at (" + iB + "%d,%d" + iB + "). They're on the same side — probably.",
}

// creepHostileWinMsgs: player defeats a hostile creep. Args: playerName, creepName, pRoll, pSum, cRoll, cSum, pct.
var creepHostileWinMsgs = []string{
	fNick + " drives off the " + iB + "%s" + iB + " " + fRoll + " vs " + fRoll + ". Phase advanced by " + fGoodPct + ".",
	"The " + iB + "%s" + iB + " retreats from " + fNick + " " + fRoll + " vs " + fRoll + ". Momentum gained: " + fGoodPct + ".",
	fNick + " tears through the " + iB + "%s" + iB + " " + fRoll + " vs " + fRoll + ". Phase advanced by " + fGoodPct + ".",
	"Signal confirmed: " + fNick + " neutralises the " + iB + "%s" + iB + " " + fRoll + " vs " + fRoll + ". Phase advanced by " + fGoodPct + ".",
	fNick + " outmanoeuvres the " + iB + "%s" + iB + " " + fRoll + " vs " + fRoll + " — it dissolves into static. Phase advanced by " + fGoodPct + ".",
}

// creepHostileLossMsgs: hostile creep defeats the player. Args: playerName, creepName, pRoll, pSum, cRoll, cSum, pct.
var creepHostileLossMsgs = []string{
	"The " + iB + "%s" + iB + " overwhelms " + fNick + " " + fRoll + " vs " + fRoll + ". Phase delayed by " + fBadPct + ".",
	fNick + " cannot repel the " + iB + "%s" + iB + " " + fRoll + " vs " + fRoll + ". Phase delayed by " + fBadPct + ".",
	"The " + iB + "%s" + iB + " tears into " + fNick + " " + fRoll + " vs " + fRoll + ". Phase delayed by " + fBadPct + ".",
	fNick + " flees the " + iB + "%s" + iB + " " + fRoll + " vs " + fRoll + " — barely. Phase delayed by " + fBadPct + ".",
	"Proximity alarm: the " + iB + "%s" + iB + " finds " + fNick + " " + fRoll + " vs " + fRoll + ". Phase delayed by " + fBadPct + ".",
}

// creepDropMsgs: player loots a defeated creep. Args: playerName, itemDesc, itemLevel, equippedNote.
var creepDropMsgs = []string{
	fNick + " salvages %s from the wreckage — level " + fLvl + "%s.",
	fNick + " recovers %s from the debris — level " + fLvl + "%s.",
	fNick + " strips %s from the dissolving core — level " + fLvl + "%s.",
	fNick + " extracts %s from the ruins — level " + fLvl + "%s.",
	fNick + " claims %s from the shattered entity — level " + fLvl + "%s.",
}

// creepPeacefulMsgs: player passes a peaceful creep without incident. Args: playerName, creepName, x, y.
var creepPeacefulMsgs = []string{
	fNick + " drifts past a " + iB + "%s" + iB + " at (" + iB + "%d,%d" + iB + "). It pays them no mind.",
	fNick + " spots a " + iB + "%s" + iB + " at (" + iB + "%d,%d" + iB + "). Neither speaks.",
	fNick + " catches a glimpse of a " + iB + "%s" + iB + " at (" + iB + "%d,%d" + iB + ") before it fades into the static.",
	fNick + " passes a " + iB + "%s" + iB + " at (" + iB + "%d,%d" + iB + "). It watches without hostility, then drifts on.",
}

// creepPeacefulBoonMsgs: player gains a bonus from a peaceful creep. Args: playerName, creepName, pct.
var creepPeacefulBoonMsgs = []string{
	fNick + " receives route data from a passing " + iB + "%s" + iB + ". Phase advanced by " + fGoodPct + ".",
	fNick + " assists a wandering " + iB + "%s" + iB + " in exchange for safe passage. Phase advanced by " + fGoodPct + ".",
	fNick + " accepts a Drift-shard from a " + iB + "%s" + iB + " as it passes. Phase advanced by " + fGoodPct + ".",
	fNick + " barters signals with a " + iB + "%s" + iB + ". The exchange favours them. Phase advanced by " + fGoodPct + ".",
}

// questReachedMsgs: quester arrives at grid target. Args: nick, qx, qy.
var questReachedMsgs = []string{
	fNick + " punches through to the objective coordinates (" + iB + "%d,%d" + iB + ").",
	fNick + " arrives at (" + iB + "%d,%d" + iB + "). One step closer.",
	fNick + " locks onto (" + iB + "%d,%d" + iB + ") — the signal is strong here.",
	fNick + " reaches (" + iB + "%d,%d" + iB + "). Holding position.",
	fNick + " confirms arrival at (" + iB + "%d,%d" + iB + "). Waiting for the others.",
	fNick + " burns through to (" + iB + "%d,%d" + iB + "). The coordinates hold.",
	fNick + " threads the Drift and emerges at (" + iB + "%d,%d" + iB + ").",
	fNick + " reaches the marked position (" + iB + "%d,%d" + iB + "). The Signal is stronger here.",
}

// Quest start/resolve message pools. Arg orders match the call sites exactly.

// Alignment constants. The int8 value is stored in Player.Alignment and
// affects battle power, crit chance, and daily events.
const (
	AlignEvil    int8 = -1
	AlignNeutral int8 = 0
	AlignGood    int8 = 1
)

// seedAlignDrift sets AlignDriftAt for neutral players that don't have one yet
// (existing characters). Threshold = current level + 1..5.
// Must be called with mu held (or before the player is shared).
func seedAlignDrift(p *Player) {
	if p.Alignment == AlignNeutral && p.AlignDriftAt == 0 {
		p.AlignDriftAt = p.Level + mathrand.Intn(5) + 1
	}
}

// alignDriftMsgs are announced when a neutral character is forced into good or
// evil alignment. %s = name, %s = "good" or "evil".
var alignDriftMsgs = []string{
	"The Void has judged %s — nobody escapes neutral forever. Alignment locked: %s.",
	"The Deep Signal renders its verdict on %s. Neutrality is a lie. Alignment: %s.",
	"Prolonged exposure to the Drift reveals %s's true nature: %s.",
	"The Pale Architects have classified %s. Neutrality denied. %s.",
	"The void cannot sustain balance in %s forever. The scales tip: %s.",
	"Protocol ZERO designates %s. No one stays neutral. Alignment: %s.",
	"The Drift strips away ambiguity from %s. What remains is %s.",
}

// alignNames maps the numeric alignment to its display string.
var alignNames = map[int8]string{
	AlignEvil:    "evil",
	AlignNeutral: "neutral",
	AlignGood:    "good",
}

// goodEventMsgs: good-alignment pair event. Args: (nick1, nick2, pct).
var goodEventMsgs = []string{
	fNick + " and " + fNick + " establish a hardened link through the noise. Shared intel accelerates both by " + fGoodPct + ".",
	"A resistance cell connects " + fNick + " and " + fNick + ". They push forward together by " + fGoodPct + ".",
	fNick + " and " + fNick + " exchange route data through a dying relay. Both gain " + fGoodPct + ".",
	"Against the static, " + fNick + " and " + fNick + " find each other's signal. Both advance by " + fGoodPct + ".",
	"A burst-transmission between " + fNick + " and " + fNick + " slips past Entity surveillance. Both gain " + fGoodPct + ".",
	fNick + " shields " + fNick + " from a Null-eddy. The goodwill is reciprocated — both advance by " + fGoodPct + ".",
	fNick + " and " + fNick + " synchronise phase-locks for a brief window. Efficiency gain: " + fGoodPct + ".",
	fNick + " and " + fNick + " share coordinates through an encrypted channel. Both gain " + fGoodPct + ".",
}

// evilStealMsgs: evil alignment item theft. Args: (evilNick, victimNick, itemLabel, itemLevel).
// itemLabel is the unique item name if the slot has one, otherwise the slot name.
var evilStealMsgs = []string{
	fNick + " transmits a targeting signal — " + fNick + "'s " + fSlot + " (level " + fLvl + ") goes dark.",
	fNick + " exploits the Drift's passage to strip " + fNick + "'s " + fSlot + " (level " + fLvl + ").",
	fNick + " uses Entity-derived methods to extract " + fNick + "'s " + fSlot + " (level " + fLvl + ") without resistance.",
	"Moving through the Null-tide, " + fNick + " tears " + fNick + "'s " + fSlot + " (level " + fLvl + ") away clean.",
	fNick + " phases through " + fNick + "'s position and leaves with their " + fSlot + " (level " + fLvl + ").",
	fNick + " activates something inherited from the Null and takes " + fNick + "'s " + fSlot + " (level " + fLvl + ").",
}

// forsakenMsgs: evil-alignment punishment. Args: (nick, pct).
var forsakenMsgs = []string{
	"The Entity " + fNick + " served discards them without ceremony. Phase delayed by " + fBadPct + ".",
	fNick + "'s alignment with the Null extracts its toll. Phase delayed by " + fBadPct + ".",
	"The Signal turns on " + fNick + ". Their compact with darkness has a price. Phase delayed by " + fBadPct + ".",
	fNick + " reaches for the Drift and finds it reaches back — hungrily. Phase delayed by " + fBadPct + ".",
	"The Entity " + fNick + " courted has grown tired of them. Phase delayed by " + fBadPct + ".",
	"What " + fNick + " bargained with collected today. Phase delayed by " + fBadPct + ".",
}

// questDescs are the mission objectives attached to quests.
var questDescs = []string{
	// Rescue & extraction
	"extract the surviving crew from the Drift-touched colony on Kerath IV",
	"escort the last xenobiologist off the compromised research station before it falls",
	"reach the Drift-stranded ship before the Null-tide rises and takes it completely",
	"pull the trapped survey team from the exclusion zone before the Entity localises them",
	"evacuate the listening post at Fracture Station before its orbit decays into the Null",
	"recover the three cryo-sleepers from the derelict generation ship before it fragments",
	"retrieve the undercover operative from deep inside the Architect tomb-complex",

	// Retrieval & data
	"decode the pre-collapse star charts buried in the dead ship's memory banks",
	"recover the black-box recorder from the vessel that crossed the Veil and did not return",
	"retrieve the last intact Architect core from the ruins of the Pale Spire",
	"retrieve Pale Architect schematics from the derelict station in the exclusion zone",
	"recover the corrupted Architect AI core before the Entity absorbs it",
	"download the Null-cartography files from the station before it is swallowed",
	"obtain the census records of the last inhabited world before they are overwritten",
	"salvage the navigation AI from the hulk drifting into the Choir's resonance zone",
	"copy the Architect's final theorem from the monument before the Drift erases it",
	"extract the memory lattice from the frozen researcher who walked into the Veil alone",

	// Destruction & denial
	"destroy the Null-seed before it consumes the station's reactor core",
	"destroy the Entity-seed germinating in the abandoned colony's deep foundations",
	"shut down the Null-broadcast before it propagates beyond the dead system",
	"silence the automated defence grid protecting the tomb of the last Architect",
	"breach the Architect relay station before it completes its transmission",
	"collapse the Veil-rift before the Choir uses it as a door",
	"detonate the resonance amplifier at the heart of the Pale Choir's congregation",
	"destroy the bridge the Entity has grown between two inhabited systems",
	"overload the Null-forge before it finishes assembling what it is building",
	"burn the archive that teaches the Entity how to dream",

	// Containment & sealing
	"sever the Signal tether anchoring the Entity to inhabited space",
	"seal the rift the Entity tore through local space before the cold gets in",
	"disable the resonance beacon drawing Entities toward the inhabited systems",
	"stabilise the collapsing Drift corridor before the next transit window closes",
	"hold the perimeter at the Fracture Point until evacuation is complete",
	"contain the Drift-bloom spreading outward from the wreck of the Obsidian Wake",
	"reinforce the boundary wards at the edge of the Exclusion Ring before the next tide",
	"anchor the waystation before Drift-shear tears it from its coordinates permanently",
	"close the wound left by the failed gate experiment before something else finds it",
	"quarantine the colony signal before the Choir follows it home",

	// Investigation & mapping
	"trace the origin of the ghost-signal looping endlessly through the relay network",
	"map the Veil-breach coordinates before they shift again and are lost",
	"prevent the Pale Choir's convergence at the coordinates marked only as The Wound",
	"determine what destroyed the survey fleet at the Kerath Margin and leave proof",
	"locate the source of the countdown signal broadcasting from uninhabited space",
	"identify which Architect is still running and what it is building toward",
	"chart the new Drift-corridor before it collapses and its route is lost forever",
	"document the Entity's feeding pattern before the last witness loses coherence",
	"investigate the station that went dark three hours after broadcasting a single word",

	// Intercept & pursuit
	"intercept the rogue Architect construct before it reaches the inhabited ring",
	"intercept the Null-courier before it delivers its cargo to the Choir's inner court",
	"pursue the ghost-ship through the Drift before it carries its passengers beyond reach",
	"catch the deserter who stole the Architect codex before they sell it to the Null",
	"overtake the autonomous Architect weapon before it reaches the civilian corridor",
	"intercept the Pale Choir's convergence fleet before it achieves formation",

	// Esoteric & strange
	"purge the Drift infestation spreading through the lower decks of the Vantareth",
	"intercept the rogue Architect construct before it reaches the inhabited ring",
	"recite the Null-breaking litany at the three relay towers before the Choir harmonises",
	"carry the last functioning Architect seed-mind to the coordinates it has requested",
	"stand witness at the Veil's edge while the last known Architect completes its death",
	"convince the Null-sovereign to look away from the inhabited systems for one hour",
	"deliver the silence-device to the heart of the Choir's song before the next verse",
	"feed the correct sequence into the Architect gate before the window closes forever",
	"answer the question the dead god-machine has been asking for three hundred years",
	"guide the last ghost-ship home before its crew forget what home means",
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

// creepSpawnCount is the number of creeps active on the grid at any time.
const creepSpawnCount = 10

// creepTemplate defines the display name, hostility, and level range for a
// creep archetype. Actual level is randomised within [MinLvl, MaxLvl] at spawn.
type creepTemplate struct {
	Name    string
	Hostile bool
	MinLvl  int
	MaxLvl  int
}

var creepTemplates = []creepTemplate{
	{"Null-wraith", true, 5, 20},
	{"Drift Pirate", true, 10, 30},
	{"Void Predator", true, 18, 45},
	{"Architect Sentinel", true, 25, 55},
	{"Signal Vampire", true, 5, 18},
	{"Entropy Hound", true, 12, 35},
	{"Phase Remnant", true, 15, 40},
	{"Pale Marauder", true, 20, 50},
	{"Wandering Archivist", false, 5, 20},
	{"Echo Drifter", false, 1, 12},
	{"Null Shepherd", false, 10, 30},
	{"Void-touched Pilgrim", false, 5, 18},
}

// Creep is a non-player entity roaming the grid. Hostile creeps attack players
// on contact; peaceful creeps may grant a small bonus or simply pass by.
type Creep struct {
	Name    string
	Hostile bool
	Level   int // item-sum equivalent used in battle rolls
	X, Y    int
}

// Player represents a registered Void Drift character. It is persisted to JSON
// and keyed by lowercase IRC nick in Game.players.
type Player struct {
	Nick   string // IRC nick, case-preserved; used for auto-login and penalty tracking
	Name   string // character display name chosen at registration; shown in all game output
	Class string // primary class, free-form text chosen at registration

	// Password is stored as a salted SHA-256 hash. PassSalt is a 16-byte
	// random value encoded as 32 hex characters. This prevents rainbow-table
	// attacks if the JSON file is ever leaked.
	PassSalt string
	PassHash string

	// Alignment affects battle power (+/-10%), crit chance, and daily events.
	// AlignDriftAt is the level at which a neutral character is forced to
	// choose good or evil. Set randomly to 5–10 at registration; 0 means unset
	// (treated as already resolved or pre-existing character).
	Alignment    int8
	AlignDriftAt int

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

	// Gender controls pronoun substitution in player-specific messages.
	// "m" = he/him/his, "f" = she/her/hers, anything else = they/them/their.
	Gender string

	Online bool   // true while the player is connected and logged in
	Addr   string // full nick!user@host mask used to identify the player on IRC

	// X, Y are the player's current position on the toroidal grid. They are
	// randomised on each login and are not persisted (position resets on reconnect).
	X, Y int

	// Activity counters used for achievements and !stats.
	BattlesWon     int
	QuestsCompleted int
	CreepsSlain    int

	// Achievements holds the IDs of earned achievements in unlock order.
	Achievements []string

	// Timestamps.
	CreatedAt time.Time
	LastLogin time.Time

	// TotalIdled is the cumulative number of seconds this player has spent
	// online with a decrementing TTL (i.e. actively idling toward a level-up).
	TotalIdled int64

	// Per-source penalty totals (in scaled seconds actually added to TTL).
	PenMesg  int64
	PenNick  int64
	PenPart  int64
	PenKick  int64
	PenQuit  int64
	PenQuest int64
	PenOther int64 // align, gender, rename, reclass, etc.
}

// they/their/them/themselves return the subject, possessive, object, and
// reflexive pronouns for the player based on their gender setting.
func (p *Player) they() string {
	switch p.Gender {
	case "m":
		return "he"
	case "f":
		return "she"
	default:
		return "they"
	}
}
func (p *Player) their() string {
	switch p.Gender {
	case "m":
		return "his"
	case "f":
		return "her"
	default:
		return "their"
	}
}
func (p *Player) them() string {
	switch p.Gender {
	case "m":
		return "him"
	case "f":
		return "her"
	default:
		return "them"
	}
}
func (p *Player) themselves() string {
	switch p.Gender {
	case "m":
		return "himself"
	case "f":
		return "herself"
	default:
		return "themselves"
	}
}
func (p *Player) they_() string { // capitalised
	switch p.Gender {
	case "m":
		return "He"
	case "f":
		return "She"
	default:
		return "They"
	}
}

// genderize replaces neutral they/their/them/themselves tokens in msg with the
// player's actual pronouns. Applied to single-player event messages after
// formatting so templates don't need per-gender variants.
func genderize(msg string, p *Player) string {
	if p.Gender == "" {
		return msg // already neutral
	}
	msg = strings.ReplaceAll(msg, "themselves", p.themselves())
	msg = strings.ReplaceAll(msg, "They", p.they_())
	msg = strings.ReplaceAll(msg, "their", p.their())
	msg = strings.ReplaceAll(msg, "Their", strings.Title(p.their()))
	msg = strings.ReplaceAll(msg, "them", p.them())
	msg = strings.ReplaceAll(msg, "they", p.they())
	return msg
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

	// lastEvent is a short description of the most recent notable game event
	// shown in the channel topic. Updated under mu; read outside mu.
	lastEvent string

	// lastTopicSet is the time the topic was last pushed to IRC, used to
	// rate-limit updates so the topic does not change every second.
	lastTopicSet time.Time

	// stopTick is closed to stop the current tick goroutine. A new channel is
	// created and a new goroutine launched on each call to start(), which
	// prevents goroutine leaks across reconnects.
	stopTick chan struct{}

	// quest holds the active quest, or nil when none is running.
	quest *Quest

	// creeps are the NPC entities currently roaming the grid.
	creeps []*Creep

	// DevMode enables accelerated testing: TTL ÷14, event rates ×10, creep
	// levels capped at 10, quest requirements reduced to 1 player / any level,
	// and auto-login of existing channel members on connect.
	// Set before start() is called; never mutated under mu.
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
	g.spawnCreeps()
	return g
}

// start stops any running tick goroutine, then launches a fresh one and
// refreshes the channel topic. Called on every successful IRC connect.
// In DevMode event rates are cranked to 10× so all game systems fire
// frequently enough to test during a short session.
func (g *Game) start() {
	if g.DevMode {
		g.Rates = Rates{PlayerEvents: 10, AlignmentEvents: 10, ServerEvents: 10}
	}
	if g.stopTick != nil {
		close(g.stopTick)
	}
	g.stopTick = make(chan struct{})
	go g.tick(g.stopTick)
	g.updateTopic()
}

// stop halts the tick loop and marks all players offline. Called on disconnect
// so game state stays consistent while the bot is not connected to IRC.
func (g *Game) stop() {
	if g.stopTick != nil {
		close(g.stopTick)
		g.stopTick = nil
	}
	g.mu.Lock()
	for _, p := range g.players {
		p.Online = false
		p.Addr = ""
	}
	g.mu.Unlock()
	g.save()
}

// OnJoin auto-logs in a registered player when they join the channel and
// announces their return. Unregistered joiners are silently ignored.
func (g *Game) OnJoin(src string) {
	nick := extractNick(src)
	g.mu.Lock()
	p := g.players[strings.ToLower(nick)]
	alreadyOnline := p != nil && p.Online
	if p != nil && !alreadyOnline {
		p.Online = true
		p.Addr = src
		p.LastLogin = time.Now()
		seedAlignDrift(p)
		// Position is randomised on every login so players cannot farm
		// encounters by repeatedly quitting and rejoining near a target.
		p.X = mathrand.Intn(gridSize)
		p.Y = mathrand.Intn(gridSize)
		g.seedAchievements(p)
	}
	g.mu.Unlock()
	if p != nil && !alreadyOnline {
		g.save()
		alignPrefix := ""
		if p.Alignment != AlignNeutral {
			alignPrefix = alignNames[p.Alignment] + " "
		}
		g.say(fmt.Sprintf(iB+cCyan+"%s"+iC+iB+", the level "+iB+"%d"+iB+" %s"+iI+"%s"+iI+", enters the void at ("+iB+"%d,%d"+iB+"). Next phase: "+iB+"%s"+iB+".",
			p.Name, p.Level, alignPrefix, p.Class, p.X, p.Y, fmtDuration(p.TTL)))
		g.updateTopic()
	}
}

// OnPart applies a p200 penalty and marks the player offline.
func (g *Game) OnPart(src string) {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p != nil {
		g.applyPenalty(p, 200, penPart)
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
		g.applyPenalty(p, 20, penQuit)
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
		g.applyPenalty(p, 30, penNick)
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
		g.applyPenalty(p, 50, penKick)
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
		g.applyPenalty(p, int64(len(text)), penMesg)
	}
	g.mu.Unlock()
	if p != nil {
		g.save()
	}
}

// CmdRegister creates a new character for the calling IRC nick with the given
// character name, password, and class. The IRC nick (from src) is used as the
// map key and for auto-login; the character name is the display name shown in
// all game messages and may differ from the IRC nick.
// Syntax: !register <name> <pass> <class…> [m|f|n]
func (g *Game) CmdRegister(src, name, pass, class, gender string) string {
	nick := extractNick(src)
	name = sanitize(name)
	if len(name) == 0 || len(name) > 32 {
		return "Character name must be 1–32 characters."
	}
	if !isValidName(name) {
		return "Character name may only contain letters, digits, hyphens, apostrophes, and dots."
	}
	class = sanitize(class)
	if len(class) == 0 || len(class) > 50 {
		return "Class must be 1–50 characters."
	}
	if !isValidName(class) {
		return "Class name may only contain letters, digits, spaces, hyphens, apostrophes, and dots."
	}
	if len(pass) == 0 || len(pass) > maxPassLen {
		return fmt.Sprintf("Password must be 1–%d characters.", maxPassLen)
	}
	key := strings.ToLower(nick)
	nameKey := strings.ToLower(name)
	if gender != "m" && gender != "f" {
		gender = "n"
	}
	now := time.Now()
	salt := newSalt()
	p := &Player{
		Nick:         nick,
		Name:         name,
		Class:        class,
		Gender:       gender,
		PassSalt:     salt,
		PassHash:     hashPass(salt, pass),
		Level:        0,
		TTL:          g.ttlForLevel(0),
		AlignDriftAt: mathrand.Intn(6) + 5, // forced alignment at level 5–10
		CreatedAt:    now,
		LastLogin:    now,
		// Auto-login: the player is clearly present since they just registered.
		Online: true,
		Addr:   src,
		X:      mathrand.Intn(gridSize),
		Y:      mathrand.Intn(gridSize),
	}
	// Hold the lock across both existence checks and the insert to prevent
	// TOCTOU races with concurrent !register calls.
	g.mu.Lock()
	_, nickTaken := g.players[key]
	var nameTaken bool
	if !nickTaken {
		for _, existing := range g.players {
			if strings.ToLower(existing.Name) == nameKey {
				nameTaken = true
				break
			}
		}
	}
	if !nickTaken && !nameTaken {
		g.players[key] = p
	}
	g.mu.Unlock()
	if nickTaken {
		return fmt.Sprintf("IRC nick %s is already registered.", nick)
	}
	if nameTaken {
		return fmt.Sprintf("Character name %q is already taken.", name)
	}
	g.save()
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" ("+iI+"%s"+iI+"), enters the void at ("+iB+"%d,%d"+iB+"). Next phase: "+iB+"%s"+iB+".",
		name, class, p.X, p.Y, fmtDuration(p.TTL))
}

// CmdLogin authenticates the player whose current IRC nick matches a registered
// character. The response is sent privately to avoid leaking "Wrong password."
// to the channel.
func (g *Game) CmdLogin(src, pass string) string {
	nick := extractNick(src)
	if len(pass) == 0 || len(pass) > maxPassLen {
		return "Invalid password."
	}
	key := strings.ToLower(nick)
	g.mu.Lock()
	p, ok := g.players[key]
	g.mu.Unlock()
	if !ok {
		return "No character registered with that nick. Use !register <name> <pass> <class> first."
	}
	// Use constant-time comparison to avoid leaking password length or prefix
	// information through timing differences.
	if subtle.ConstantTimeCompare([]byte(p.PassHash), []byte(hashPass(p.PassSalt, pass))) != 1 {
		return "Wrong password."
	}
	g.mu.Lock()
	p.Online = true
	p.Addr = src
	p.LastLogin = time.Now()
	g.seedAchievements(p)
	seedAlignDrift(p)
	g.mu.Unlock()
	g.save()
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+", the level "+iB+"%d"+iB+" "+iI+"%s"+iI+", logged in. Next phase: "+iB+"%s"+iB+".", p.Name, p.Level, p.Class, fmtDuration(p.TTL))
}

// CmdPasswd changes the password for the calling player after verifying the
// current password. The player must be online (logged in) to use this command.
func (g *Game) CmdPasswd(src, oldPass, newPass string) string {
	nick := extractNick(src)
	g.mu.Lock()
	p := g.players[strings.ToLower(nick)]
	g.mu.Unlock()
	if p == nil {
		return "No character registered with that nick."
	}
	if !p.Online || p.Addr != src {
		return "You must be logged in to change your password."
	}
	if subtle.ConstantTimeCompare([]byte(p.PassHash), []byte(hashPass(p.PassSalt, oldPass))) != 1 {
		return "Wrong current password."
	}
	salt := newSalt()
	g.mu.Lock()
	p.PassSalt = salt
	p.PassHash = hashPass(salt, newPass)
	g.mu.Unlock()
	g.save()
	return "Password updated."
}

// CmdLogout marks the calling player offline. No penalty is applied.
// CmdGender changes the player's gender pronoun setting. Costs p50 — altering
// one's cosmic designation is not without consequence.
func (g *Game) CmdGender(src, gender string) string {
	nick := extractNick(src)
	g.mu.Lock()
	p := g.players[strings.ToLower(nick)]
	if p == nil || !p.Online || p.Addr != src {
		g.mu.Unlock()
		return "You must be logged in to change your gender."
	}
	if gender != "m" && gender != "f" && gender != "n" {
		g.mu.Unlock()
		return "Valid options: m (he/him), f (she/her), n (they/them)."
	}
	if p.Gender == gender {
		g.mu.Unlock()
		return "That is already your designation."
	}
	p.Gender = gender
	g.applyPenalty(p, 50, penOther)
	name := p.Name
	ttl := p.TTL
	g.mu.Unlock()
	g.save()
	pronouns := map[string]string{"m": "he/him", "f": "she/her", "n": "they/them"}
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" reconfigures their designation to "+iB+"%s"+iB+". The realignment costs them. Next phase: "+iB+"%s"+iB+".",
		name, pronouns[gender], fmtDuration(ttl))
}

func (g *Game) CmdLogout(src string) string {
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
	return fmt.Sprintf("%s has disconnected from the Void Drift.", p.Name)
}

// CmdDelete permanently removes the calling player's account. Requires password
// confirmation. Removes them from any guild (transferring leadership if needed)
// and announces the deletion to the channel.
func (g *Game) CmdDelete(src, pass string) string {
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	if subtle.ConstantTimeCompare([]byte(p.PassHash), []byte(hashPass(p.PassSalt, pass))) != 1 {
		g.mu.Unlock()
		return "Wrong password."
	}

	nick := strings.ToLower(p.Nick)
	name := p.Name

	// Remove from guild, transferring leadership or disbanding if necessary.
	guild := g.playerGuild(nick)
	var guildMsg string
	if guild != nil {
		guildName := guild.Name
		guildKey := strings.ToLower(strings.Join(strings.Fields(guildName), " "))
		guild.removeMember(nick)
		if len(guild.Members) == 0 {
			delete(g.guilds, guildKey)
			guildMsg = fmt.Sprintf(iB+"[%s]"+iB+" has been disbanded.", guildName)
		} else if guild.Leader == nick {
			guild.Leader = guild.Members[0]
			guildMsg = fmt.Sprintf(iB+"[%s]"+iB+" command transfers to "+iB+cCyan+"%s"+iC+iB+".", guildName, guild.Leader)
		}
	}

	delete(g.players, nick)
	g.mu.Unlock()
	g.save()
	g.saveGuilds()

	msg := fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" has been erased from the Void Drift. The record is gone.", name)
	if guildMsg != "" {
		msg += " " + guildMsg
	}
	return msg
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
		g.applyPenalty(p, 75, penOther)
	}
	g.mu.Unlock()
	g.save()
	if changed {
		return fmt.Sprintf("%s is now %s. Changing alignment costs time — phase adjusted.", p.Name, alignNames[newAlign])
	}
	return fmt.Sprintf("%s is already %s.", p.Name, alignNames[newAlign])
}

// CmdRename changes the calling player's character name. Costs p100.
func (g *Game) CmdRename(src, newName string) string {
	newName = sanitize(newName)
	if newName == "" || strings.ContainsAny(newName, " \t") {
		return "Usage: !rename <name>  — one word, no spaces"
	}
	if len(newName) > 50 {
		return "Name must be 50 characters or fewer."
	}
	if !isValidName(newName) {
		return "Name may only contain letters, digits, hyphens, apostrophes, and dots."
	}
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	if p.Name == newName {
		g.mu.Unlock()
		return "That is already your name."
	}
	oldName := p.Name
	p.Name = newName
	g.applyPenalty(p, 100, penOther)
	ttl := p.TTL
	g.mu.Unlock()
	g.save()
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" is now known as "+iB+cCyan+"%s"+iC+iB+". Rewriting the record costs them. Next phase: "+iB+"%s"+iB+".",
		oldName, newName, fmtDuration(ttl))
}

// CmdReclass changes the calling player's primary class. Costs p100.
// The focus slot will shift, which may affect battle effectiveness.
func (g *Game) CmdReclass(src, newClass string) string {
	newClass = sanitize(newClass)
	if newClass == "" || strings.ContainsAny(newClass, " \t") {
		return "Usage: !reclass <class>  — one word, no spaces"
	}
	if len(newClass) > 50 {
		return "Class name must be 50 characters or fewer."
	}
	if !isValidName(newClass) {
		return "Class name may only contain letters, digits, hyphens, apostrophes, and dots."
	}
	g.mu.Lock()
	p := g.findByAddr(src)
	if p == nil {
		g.mu.Unlock()
		return "You are not logged in."
	}
	if p.Class == newClass {
		g.mu.Unlock()
		return "That is already your class."
	}
	oldClass := p.Class
	p.Class = newClass
	g.applyPenalty(p, 100, penOther)
	name := p.Name
	slot := classFocusSlot(newClass)
	ttl := p.TTL
	g.mu.Unlock()
	g.save()
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" abandons "+iB+"%s"+iB+" and becomes a "+iB+"%s"+iB+". Focus shifts to "+iB+"%s"+iB+". Next phase: "+iB+"%s"+iB+".",
		name, oldClass, newClass, itemLabel(p, slot), fmtDuration(ttl))
}

// CmdStatus returns a one-line status summary for the target player. If
// targetNick is empty, it reports on the calling player.
func (g *Game) CmdStatus(src, targetNick string) string {
	if targetNick == "" {
		targetNick = extractNick(src)
	}
	p := g.findPlayer(targetNick)
	if p == nil {
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
	titleDisplay := ""
	if t := earnedTitle(p); t != "" {
		titleDisplay = " " + iB + "[" + t + "]" + iB
	}
	return fmt.Sprintf(iB+cCyan+"%s"+iC+iB+"%s, the level "+iB+"%d"+iB+" %s "+iI+"%s"+iI+" [%s]%s — phase: "+iB+"%s"+iB+" — Items: "+iB+"%d"+iB+" (focus: %s)",
		p.Name, titleDisplay, p.Level, alignNames[p.Alignment], classDisplay, status, questInfo,
		fmtDuration(p.TTL), p.itemSum(), focusDisplay)
}

// CmdStats returns a multi-line stats breakdown for the target player.
// If targetNick is empty, reports on the calling player.
func (g *Game) CmdStats(src, targetNick string) []string {
	if targetNick == "" {
		targetNick = extractNick(src)
	}
	p := g.findPlayer(targetNick)
	if p == nil {
		return []string{fmt.Sprintf("No character found for %s.", targetNick)}
	}

	created := "unknown"
	if !p.CreatedAt.IsZero() {
		created = p.CreatedAt.Format("2006-01-02")
	}
	lastLogin := "never"
	if !p.LastLogin.IsZero() {
		lastLogin = p.LastLogin.Format("2006-01-02 15:04 UTC")
	}

	total := p.penTotal()
	penPct := func(v int64) string {
		if total == 0 {
			return "0%"
		}
		return fmt.Sprintf("%d%%", v*100/total)
	}

	return []string{
		fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" — stats (level "+iB+"%d"+iB+", account created %s)",
			p.Name, p.Level, created),
		fmt.Sprintf("  Idled: "+iB+"%s"+iB+" total  |  Last login: %s",
			fmtDuration(p.TotalIdled), lastLogin),
		fmt.Sprintf("  Penalties total: "+iB+"%s"+iB+"  |  mesg %s  nick %s  part %s  kick %s  quit %s  quest %s  other %s",
			fmtDuration(total),
			penPct(p.PenMesg), penPct(p.PenNick), penPct(p.PenPart),
			penPct(p.PenKick), penPct(p.PenQuit), penPct(p.PenQuest), penPct(p.PenOther)),
	}
}

// CmdPos returns the grid coordinates of the target player and lists any
// co-located players sharing the same tile. If targetNick is empty, reports
// on the calling player.
func (g *Game) CmdPos(src, targetNick string) string {
	if targetNick == "" {
		targetNick = extractNick(src)
	}
	p := g.findPlayer(targetNick)
	if p == nil {
		return fmt.Sprintf("No character found for %s.", targetNick)
	}
	g.mu.Lock()
	if !p.Online {
		g.mu.Unlock()
		return fmt.Sprintf("%s is offline and has no position.", p.Name)
	}
	x, y, name := p.X, p.Y, p.Name

	var neighbours []string
	for _, op := range g.players {
		if op != p && op.Online && op.X == x && op.Y == y {
			neighbours = append(neighbours, op.Name)
		}
	}

	questNote := ""
	if g.quest != nil && g.quest.IsGrid && g.quest.QX == x && g.quest.QY == y {
		questNote = " [quest destination!]"
	}
	g.mu.Unlock()

	info := fmt.Sprintf("%s is at (%d,%d)%s on a %d×%d grid.", name, x, y, questNote, gridSize, gridSize)
	if len(neighbours) > 0 {
		info += fmt.Sprintf(" Also here: %s.", strings.Join(neighbours, ", "))
	}
	return info
}

// mapRadiusX and mapRadiusY define the half-width and half-height of the
// ASCII minimap rendered by CmdMap. The full view is (2*X+1) × (2*Y+1).
const (
	mapRadiusX = 5
	mapRadiusY = 3
)

// CmdMap renders a small ASCII minimap centred on the calling player and
// returns it as a []string, one element per IRC line. Must be called
// with the player logged in; returns a single error string otherwise.
func (g *Game) CmdMap(src string) []string {
	nick := extractNick(src)
	g.mu.Lock()
	self := g.players[strings.ToLower(nick)]
	if self == nil || !self.Online {
		g.mu.Unlock()
		return []string{"You must be logged in to use !map."}
	}

	sx, sy := self.X, self.Y

	// Snapshot online players and creeps while holding the lock.
	type playerDot struct {
		x, y  int
		label byte
	}
	var dots []playerDot
	for _, p := range g.players {
		if !p.Online || strings.ToLower(p.Nick) == strings.ToLower(nick) {
			continue
		}
		label := byte('P')
		if len(p.Name) > 0 {
			label = p.Name[0]
			if label >= 'a' && label <= 'z' {
				label -= 32
			}
		}
		dots = append(dots, playerDot{p.X, p.Y, label})
	}

	type creepDot struct {
		x, y    int
		hostile bool
		name    string
	}
	var cdots []creepDot
	for _, c := range g.creeps {
		cdots = append(cdots, creepDot{c.X, c.Y, c.Hostile, c.Name})
	}

	var questX, questY int
	hasQuest := g.quest != nil && g.quest.IsGrid
	if hasQuest {
		questX, questY = g.quest.QX, g.quest.QY
	}
	g.mu.Unlock()

	cols := 2*mapRadiusX + 1
	rows := 2*mapRadiusY + 1

	// cell builds the display character for the map tile at absolute grid
	// position (ax, ay). Priority: self > other players > hostile creep >
	// peaceful creep > quest target > empty.
	cell := func(ax, ay int) string {
		if ax == sx && ay == sy {
			return iB + cCyan + "@" + iC + iB
		}
		for _, d := range dots {
			if d.x == ax && d.y == ay {
				return iB + string([]byte{d.label}) + iB
			}
		}
		for _, c := range cdots {
			if c.x == ax && c.y == ay {
				if c.hostile {
					return iB + cRed + "!" + iC + iB
				}
				return iB + cTeal + "~" + iC + iB
			}
		}
		if hasQuest && ax == questX && ay == questY {
			return iB + cLime + "*" + iC + iB
		}
		return "·"
	}

	border := "+" + strings.Repeat("-", cols) + "+"
	lines := make([]string, 0, rows+3)
	lines = append(lines, border)
	for dy := -mapRadiusY; dy <= mapRadiusY; dy++ {
		row := "|"
		for dx := -mapRadiusX; dx <= mapRadiusX; dx++ {
			ax := (sx + dx + gridSize) % gridSize
			ay := (sy + dy + gridSize) % gridSize
			row += cell(ax, ay)
		}
		row += "|"
		lines = append(lines, row)
	}
	lines = append(lines, border)

	// Build a legend of visible entities.
	var legend []string
	legend = append(legend, fmt.Sprintf("(%d,%d)", sx, sy))
	seenPlayers := map[byte]bool{}
	for _, d := range dots {
		dx := d.x - sx
		dy := d.y - sy
		// Adjust for toroidal wrap.
		if dx > gridSize/2 {
			dx -= gridSize
		} else if dx < -gridSize/2 {
			dx += gridSize
		}
		if dy > gridSize/2 {
			dy -= gridSize
		} else if dy < -gridSize/2 {
			dy += gridSize
		}
		if dx >= -mapRadiusX && dx <= mapRadiusX && dy >= -mapRadiusY && dy <= mapRadiusY {
			if !seenPlayers[d.label] {
				seenPlayers[d.label] = true
				legend = append(legend, string([]byte{d.label})+":player")
			}
		}
	}
	for _, c := range cdots {
		dx := c.x - sx
		dy := c.y - sy
		if dx > gridSize/2 {
			dx -= gridSize
		} else if dx < -gridSize/2 {
			dx += gridSize
		}
		if dy > gridSize/2 {
			dy -= gridSize
		} else if dy < -gridSize/2 {
			dy += gridSize
		}
		if dx >= -mapRadiusX && dx <= mapRadiusX && dy >= -mapRadiusY && dy <= mapRadiusY {
			sym := "~"
			if c.hostile {
				sym = "!"
			}
			legend = append(legend, sym+":"+c.name)
		}
	}
	if hasQuest {
		qdx := questX - sx
		qdy := questY - sy
		if qdx > gridSize/2 {
			qdx -= gridSize
		} else if qdx < -gridSize/2 {
			qdx -= gridSize
		}
		if qdy > gridSize/2 {
			qdy -= gridSize
		} else if qdy < -gridSize/2 {
			qdy -= gridSize
		}
		if qdx >= -mapRadiusX && qdx <= mapRadiusX && qdy >= -mapRadiusY && qdy <= mapRadiusY {
			legend = append(legend, "*:quest target")
		}
	}
	lines = append(lines, strings.Join(legend, " | "))
	return lines
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
		parts[i] = fmt.Sprintf("%d. %s (lvl %d, items %d)", i+1, p.Name, p.Level, p.itemSum())
	}
	return "Top players: " + strings.Join(parts, " | ")
}

// CmdAll returns all registered players sorted by level descending, then TTL
// ascending, one line per player. Online players are marked with *.
func (g *Game) CmdAll() []string {
	players := g.sortedPlayers()
	if len(players) == 0 {
		return []string{"No players yet."}
	}
	lines := make([]string, len(players))
	for i, p := range players {
		online := " "
		if p.Online {
			online = "*"
		}
		lines[i] = fmt.Sprintf("%d.%s %s — lvl %d %s — next: %s", i+1, online, p.Name, p.Level, p.Class, fmtDuration(p.TTL))
	}
	return lines
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
		names[i] = p.Name
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
			parts = append(parts, fmt.Sprintf("%s (lvl %d)", p.Name, p.Level))
		}
	}
	g.mu.Unlock()

	if len(parts) == 0 {
		return "No players currently online."
	}
	sort.Strings(parts)
	return fmt.Sprintf("Online (%d): %s", len(parts), strings.Join(parts, ", "))
}

// IsKnownOffline reports whether nick belongs to a registered player who is not
// currently in the game channel. Used to decide whether to send an IRC INVITE.
func (g *Game) IsKnownOffline(nick string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	p, ok := g.players[strings.ToLower(nick)]
	return ok && !p.Online
}

// SuggestForNick returns a two-element slice with a welcome message and a
// registration hint for an unregistered nick, or nil if the nick already has
// a character. Each element is sent as a separate PRIVMSG by the JOIN handler.
func (g *Game) SuggestForNick(nick string) []string {
	g.mu.Lock()
	_, registered := g.players[strings.ToLower(nick)]
	taken := g.takenNames()
	g.mu.Unlock()
	if registered {
		return nil
	}
	name, class := generateSuggestion(taken)
	return []string{
		"Welcome to Void Drift — you are not yet registered.",
		fmt.Sprintf("Suggestion: !register %s <password> %s [m/f/n]  — or choose your own name and class.", name, class),
	}
}

// Suggest returns a fresh name/class suggestion for use with the !suggest command.
func (g *Game) Suggest() string {
	g.mu.Lock()
	taken := g.takenNames()
	g.mu.Unlock()
	name, class := generateSuggestion(taken)
	return fmt.Sprintf("Suggestion: !register %s <password> %s [m/f/n]  — or choose your own name and class.", name, class)
}

// takenNames returns a set of lowercase character names currently in use.
// Must be called with mu held.
func (g *Game) takenNames() map[string]struct{} {
	taken := make(map[string]struct{}, len(g.players))
	for _, p := range g.players {
		taken[strings.ToLower(p.Name)] = struct{}{}
	}
	return taken
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

		levelUps, msgs, driftSave := g.tickPlayers(online)
		battlePairs, tradePairs, gridMsgs := g.tickGrid(online)
		msgs = append(msgs, gridMsgs...)
		msgs = append(msgs, g.tickCreeps(online)...)
		msgs = append(msgs, g.tickQuestProgress(online)...)
		msgs = append(msgs, g.tickServerEvents(online)...)

		topicWorthy := len(levelUps) > 0 || len(battlePairs) > 0 || len(tradePairs) > 0
		notableEvent := false
		if ev := g.captureNotableEvent(msgs); ev != "" {
			g.lastEvent = ev
			topicWorthy = true
			notableEvent = true
		}

		g.mu.Unlock()

		for _, msg := range msgs {
			g.say(msg)
		}
		// Encounters trigger a standard 1v1 battle outside the lock because
		// battle() acquires mu internally.
		for _, ep := range battlePairs {
			g.battle(ep[0], ep[1])
		}
		for _, ep := range tradePairs {
			g.doTrade(ep[0], ep[1])
		}
		for _, p := range levelUps {
			g.doLevelUp(p)
		}
		if len(levelUps) > 0 || driftSave {
			g.save()
		}
		if notableEvent {
			g.pushTopic()
		} else if topicWorthy {
			g.updateTopic()
		}
	}
}

// tickPlayers decrements TTL for each online player, queues those whose TTL
// has reached zero for level-up, and fires per-player random/bot-battle/
// alignment events. Must be called with mu held.
func (g *Game) tickPlayers(online []*Player) (levelUps []*Player, msgs []string, needsSave bool) {
	for _, p := range online {
		p.TTL--
		p.TotalIdled++
		msgs = append(msgs, g.checkAchievements(p)...)
		if p.TTL <= 0 {
			levelUps = append(levelUps, p)
			continue
		}
		// Forced alignment drift: neutral players are assigned good or evil
		// when they reach their AlignDriftAt level.
		if p.Alignment == AlignNeutral && p.AlignDriftAt > 0 && p.Level >= p.AlignDriftAt {
			if mathrand.Intn(2) == 0 {
				p.Alignment = AlignGood
			} else {
				p.Alignment = AlignEvil
			}
			p.AlignDriftAt = 0 // mark resolved
			needsSave = true
			tmpl := alignDriftMsgs[mathrand.Intn(len(alignDriftMsgs))]
			msgs = append(msgs, eventHeader("🎲", "ALIGNMENT DRIFT"))
			msgs = append(msgs, genderize(fmt.Sprintf(tmpl, p.Name, alignNames[p.Alignment]), p))
		}
		// ~6/day: random individual event (calamity, godsend, item change, find item).
		if rateCheck(86400/6, g.Rates.PlayerEvents) {
			msgs = append(msgs, g.randomEvent(p))
		}
		// ~2/day: 1v1 challenge against the bot.
		if rateCheck(86400/2, g.Rates.PlayerEvents) {
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

// driftStep returns a ±1 step biased toward the target coordinate on a
// toroidal axis of size gridSize. With 70% probability it steps toward the
// target (taking the shorter arc); otherwise it steps randomly.
func driftStep(pos, target int) int {
	direct := 0
	d := target - pos
	// Choose the shorter arc on the toroidal axis.
	if d > gridSize/2 {
		d -= gridSize
	} else if d < -gridSize/2 {
		d += gridSize
	}
	if d > 0 {
		direct = 1
	} else if d < 0 {
		direct = -1
	}
	if direct != 0 && mathrand.Intn(10) < 7 {
		return direct
	}
	return mathrand.Intn(3) - 1
}

// tickGrid moves every online player one step in a random direction on the
// toroidal map and checks for co-tile encounters. Returns up to one battle or
// trade pair per tick (to prevent message flooding) and any announcement
// messages. Must be called with mu held.
func (g *Game) tickGrid(online []*Player) (battlePairs, tradePairs [][2]*Player, msgs []string) {
	// Build a set of quester nicks for O(1) lookup.
	questerNicks := make(map[string]bool)
	if g.quest != nil && g.quest.IsGrid {
		for _, qp := range g.quest.Questers {
			questerNicks[strings.ToLower(qp.Nick)] = true
		}
	}

	// Build a position map after moving everyone.
	posMap := make(map[[2]int][]*Player, len(online))
	for _, p := range online {
		var dx, dy int
		if questerNicks[strings.ToLower(p.Nick)] && g.quest != nil {
			// Questers drift toward the target; others walk randomly.
			dx = driftStep(p.X, g.quest.QX)
			dy = driftStep(p.Y, g.quest.QY)
		} else {
			dx = mathrand.Intn(3) - 1
			dy = mathrand.Intn(3) - 1
		}
		p.X = (p.X + dx + gridSize) % gridSize
		p.Y = (p.Y + dy + gridSize) % gridSize
		key := [2]int{p.X, p.Y}
		posMap[key] = append(posMap[key], p)
	}

	// Encounter probability scales with the crowd: 1/len(online) per shared
	// tile, so larger populations see proportionally fewer surprise fights.
	if len(online) > 0 {
		for _, group := range posMap {
			if len(group) >= 2 && mathrand.Intn(len(online)) == 0 {
				mathrand.Shuffle(len(group), func(i, j int) { group[i], group[j] = group[j], group[i] })
				a, b := group[0], group[1]

				ga := g.playerGuild(strings.ToLower(a.Nick))
				gb := g.playerGuild(strings.ToLower(b.Nick))
				sameGuild := ga != nil && gb != nil && ga == gb

				if sameGuild {
					// Guild-mates: 15% spar, 50% trade, 35% pass-by (roll out of 20).
					roll := mathrand.Intn(20)
					switch {
					case roll < 3: // 15% friendly spar
						battlePairs = append(battlePairs, [2]*Player{a, b})
						msgs = append(msgs, fmt.Sprintf(guildSparMsgs[mathrand.Intn(len(guildSparMsgs))],
							a.Name, b.Name, a.X, a.Y))
					case roll < 13: // 50% cooperative trade
						tradePairs = append(tradePairs, [2]*Player{a, b})
						msgs = append(msgs, fmt.Sprintf(guildTradeMsgs[mathrand.Intn(len(guildTradeMsgs))],
							a.Name, b.Name, a.X, a.Y))
					default: // 35% check-in pass-by
						msgs = append(msgs, fmt.Sprintf(guildPassByMsgs[mathrand.Intn(len(guildPassByMsgs))],
							a.Name, b.Name, a.X, a.Y))
					}
				} else {
					roll := mathrand.Intn(10)
					switch {
					case roll < 5: // 50% battle
						battlePairs = append(battlePairs, [2]*Player{a, b})
						msgs = append(msgs, fmt.Sprintf(encounterMsgs[mathrand.Intn(len(encounterMsgs))],
							a.Name, b.Name, a.X, a.Y))
					case roll < 8: // 30% trade
						tradePairs = append(tradePairs, [2]*Player{a, b})
						msgs = append(msgs, fmt.Sprintf(tradeMsgs[mathrand.Intn(len(tradeMsgs))],
							a.Name, b.Name, a.X, a.Y))
					default: // 20% pass-by
						msgs = append(msgs, fmt.Sprintf(passByMsgs[mathrand.Intn(len(passByMsgs))],
							a.Name, b.Name, a.X, a.Y))
					}
				}
				break // one encounter per tick to avoid flooding
			}
		}
	}
	return
}

// newCreep rolls a random creep from the template list. In DevMode levels are
// capped at 10 so low-level players can realistically fight them.
func (g *Game) newCreep() *Creep {
	tmpl := creepTemplates[mathrand.Intn(len(creepTemplates))]
	maxLvl := tmpl.MaxLvl
	if g.DevMode && maxLvl > 10 {
		maxLvl = 10
	}
	minLvl := tmpl.MinLvl
	if minLvl > maxLvl {
		minLvl = maxLvl
	}
	lvl := minLvl + mathrand.Intn(maxLvl-minLvl+1)
	return &Creep{
		Name:    tmpl.Name,
		Hostile: tmpl.Hostile,
		Level:   lvl,
		X:       mathrand.Intn(gridSize),
		Y:       mathrand.Intn(gridSize),
	}
}

// spawnCreeps populates g.creeps with creepSpawnCount newly placed creeps
// drawn randomly from creepTemplates. Safe to call without mu (called before
// start() launches the tick goroutine).
func (g *Game) spawnCreeps() {
	g.creeps = make([]*Creep, 0, creepSpawnCount)
	for i := 0; i < creepSpawnCount; i++ {
		g.creeps = append(g.creeps, g.newCreep())
	}
}

// respawnCreep replaces c with a freshly rolled creep at a random grid
// position. Must be called with mu held.
func (g *Game) respawnCreep(idx int) {
	g.creeps[idx] = g.newCreep()
}

// creepDrop rolls a potential item drop for a player who just defeated a
// hostile creep. Drop chance is 40%; item level is 1–creep.Level so stronger
// creeps drop better loot. Must be called with mu held.
func (g *Game) creepDrop(p *Player, c *Creep) string {
	if mathrand.Intn(10) >= 4 { // 60% no drop
		return ""
	}
	slot := mathrand.Intn(10)
	slotName := itemSlots[slot]
	itemLevel := mathrand.Intn(c.Level) + 1
	itemName := generateItemName(rarityNormal, slotName)
	equippedNote := ""
	if itemLevel > p.Items[slot] {
		p.Items[slot] = itemLevel
		p.ItemNames[slot] = itemName
		equippedNote = " (equipped)"
	}
	return fmt.Sprintf(creepDropMsgs[mathrand.Intn(len(creepDropMsgs))],
		p.Name, articleFor(itemName)+" "+itemName, itemLevel, equippedNote)
}

// tickCreeps moves every creep one step and checks for player encounters.
// Hostile creeps battle any co-tile player; peaceful creeps may grant a small
// bonus. At most one creep encounter fires per tick to avoid flooding.
// Must be called with mu held.
func (g *Game) tickCreeps(online []*Player) []string {
	if len(online) == 0 {
		// Still move the creeps so they wander even while nobody is logged in.
		for _, c := range g.creeps {
			c.X = (c.X + mathrand.Intn(3) - 1 + gridSize) % gridSize
			c.Y = (c.Y + mathrand.Intn(3) - 1 + gridSize) % gridSize
		}
		return nil
	}

	// Build a player position lookup keyed by (x,y).
	posMap := make(map[[2]int][]*Player, len(online))
	for _, p := range online {
		key := [2]int{p.X, p.Y}
		posMap[key] = append(posMap[key], p)
	}

	var msgs []string
	encountered := false
	for idx, c := range g.creeps {
		c.X = (c.X + mathrand.Intn(3) - 1 + gridSize) % gridSize
		c.Y = (c.Y + mathrand.Intn(3) - 1 + gridSize) % gridSize

		if encountered {
			continue // one encounter per tick
		}
		players := posMap[[2]int{c.X, c.Y}]
		if len(players) == 0 {
			continue
		}
		// Probability: 1/max(len(online),3) so busier grids don't flood.
		denom := len(online)
		if denom < 3 {
			denom = 3
		}
		if mathrand.Intn(denom) != 0 {
			continue
		}

		p := players[mathrand.Intn(len(players))]
		encountered = true

		if c.Hostile {
			pSum := effectiveItemSum(p)
			if pSum < 1 {
				pSum = 1
			}
			cSum := c.Level
			if cSum < 1 {
				cSum = 1
			}
			pRoll := mathrand.Intn(pSum)
			cRoll := mathrand.Intn(cSum)
			if pRoll >= cRoll {
				pct := mathrand.Intn(11) + 10 // 10–20%
				change := p.TTL * int64(pct) / 100
				if change < 1 {
					change = 1
				}
				p.TTL -= change
				if p.TTL < 1 {
					p.TTL = 1
				}
				tmpl := creepHostileWinMsgs[mathrand.Intn(len(creepHostileWinMsgs))]
				msgs = append(msgs, fmt.Sprintf(tmpl, p.Name, c.Name, pRoll, pSum, cRoll, cSum, pct))
				if drop := g.creepDrop(p, c); drop != "" {
					msgs = append(msgs, drop)
				}
				p.CreepsSlain++
				msgs = append(msgs, g.checkAchievements(p)...)
				g.respawnCreep(idx)
			} else {
				pct := mathrand.Intn(8) + 7 // 7–14%
				change := p.TTL * int64(pct) / 100
				if change < 1 {
					change = 1
				}
				p.TTL += change
				tmpl := creepHostileLossMsgs[mathrand.Intn(len(creepHostileLossMsgs))]
				msgs = append(msgs, fmt.Sprintf(tmpl, p.Name, c.Name, pRoll, pSum, cRoll, cSum, pct))
			}
		} else {
			// Peaceful: 40% chance of a small TTL boon, 60% flavour pass-by.
			if mathrand.Intn(10) < 4 {
				pct := mathrand.Intn(6) + 5 // 5–10%
				change := p.TTL * int64(pct) / 100
				if change < 1 {
					change = 1
				}
				p.TTL -= change
				if p.TTL < 1 {
					p.TTL = 1
				}
				tmpl := creepPeacefulBoonMsgs[mathrand.Intn(len(creepPeacefulBoonMsgs))]
				msgs = append(msgs, fmt.Sprintf(tmpl, p.Name, c.Name, pct))
			} else {
				tmpl := creepPeacefulMsgs[mathrand.Intn(len(creepPeacefulMsgs))]
				msgs = append(msgs, fmt.Sprintf(tmpl, p.Name, c.Name, c.X, c.Y))
			}
		}
	}
	return msgs
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
			msgs = append(msgs, fmt.Sprintf(questReachedMsgs[mathrand.Intn(len(questReachedMsgs))],
				qp.Name, g.quest.QX, g.quest.QY))
		}
	}
	if len(g.quest.Reached) == len(g.quest.Questers) {
		msgs = append(msgs, g.resolveQuest(online)...)
		g.quest = nil
	}
	return msgs
}

// tickServerEvents fires the server-wide periodic events: Hand of God (~1/day),
// team battle (~8/day when 6+ online), guild battle (~4/day), quest
// start (~4/day), and quest timeout resolution. Must be called with mu held.
func (g *Game) tickServerEvents(online []*Player) []string {
	var msgs []string
	if len(online) > 0 && rateCheck(86400, g.Rates.ServerEvents) {
		msgs = append(msgs, g.handOfGod(online[mathrand.Intn(len(online))])...)
	}
	if len(online) >= 2 && rateCheck(86400*2, g.Rates.ServerEvents) {
		msgs = append(msgs, g.voidStorm(online)...)
	}
	if len(online) >= 6 && rateCheck(86400/8, g.Rates.ServerEvents) {
		msgs = append(msgs, g.teamBattle(online)...)
	}
	if rateCheck(86400/4, g.Rates.ServerEvents) {
		msgs = append(msgs, g.guildBattle()...)
	}
	if g.quest == nil && rateCheck(86400/4, g.Rates.ServerEvents) {
		msgs = append(msgs, g.tryStartQuest(online)...)
	}
	if g.quest != nil && time.Now().After(g.quest.EndsAt) {
		msgs = append(msgs, g.resolveQuest(online)...)
		g.quest = nil
	}
	if rateCheck(86400, g.Rates.ServerEvents) {
		msgs = append(msgs, promoMsgs[mathrand.Intn(len(promoMsgs))])
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
	name := p.Name
	gender := p.Gender
	level := p.Level
	ttl := p.TTL
	isum := p.itemSum()

	// Collect eligible opponents and check achievements while the lock is held.
	online := g.onlinePlayers()
	var opponents []*Player
	for _, op := range online {
		if strings.ToLower(op.Nick) != strings.ToLower(p.Nick) {
			opponents = append(opponents, op)
		}
	}
	achMsgs := g.checkAchievements(p)
	g.mu.Unlock()

	itemDesc := slotName
	if itemName != "" {
		itemDesc = itemName
	}
	itemPhrase := fmt.Sprintf("%s lv%d %s", articleFor(itemDesc), itemLevel, itemDesc)
	equipped := ""
	if improved {
		equipped = " (equipped!)"
	}
	label := ""
	if itemRarity != rarityNormal {
		label = " " + rarityLabel(itemRarity)
	}
	tmpl := levelUpItemMsgs[mathrand.Intn(len(levelUpItemMsgs))]
	g.say(genderize(tmpl(name, fmt.Sprintf("%d", level), fmtDuration(ttl),
		itemPhrase, equipped, label)+
		fmt.Sprintf(" [item total: "+iB+"%d"+iB+"]", isum), &Player{Gender: gender}))

	switch itemRarity {
	case rarityVoidEternal:
		g.noteEvent(fmt.Sprintf("✦ %s found %s — VOID-ETERNAL!", name, itemName))
	case rarityArchitect:
		g.noteEvent(fmt.Sprintf("★ %s found %s (Architect-grade) at lvl %d", name, itemName, level))
	case rarityReclaimed:
		g.noteEvent(fmt.Sprintf("%s reached lvl %d, found %s (Reclaimed)", name, level, itemName))
	default:
		// Regular level-ups are common; store the event but only push the topic
		// if the rate limit allows, to avoid flooding the IRC topic every minute.
		g.mu.Lock()
		g.lastEvent = fmt.Sprintf("%s reached lvl %d", name, level)
		g.mu.Unlock()
		g.updateTopic()
	}

	for _, m := range achMsgs {
		g.say(m)
	}
	if len(opponents) > 0 {
		g.battle(p, opponents[mathrand.Intn(len(opponents))])
	}
}

// battle runs a standard 1v1 fight between a and b. Each side rolls
// doTrade attempts a mutually beneficial item exchange between two players who
// met on the grid. It finds a pair of slots (i, j) where a has a higher item
// in slot i and b has a higher item in slot j, then swaps those two items so
// both players improve. If no such pair exists it falls back to a battle.
// Acquires mu internally.
func (g *Game) doTrade(a, b *Player) {
	g.mu.Lock()
	type slotPair struct{ i, j int }
	var candidates []slotPair
	for i := 0; i < 10; i++ {
		for j := 0; j < 10; j++ {
			if i == j {
				continue
			}
			if a.Items[i] > b.Items[i] && b.Items[j] > a.Items[j] {
				candidates = append(candidates, slotPair{i, j})
			}
		}
	}
	if len(candidates) == 0 {
		g.mu.Unlock()
		// No mutually beneficial trade; fall back to battle.
		g.battle(a, b)
		return
	}
	pick := candidates[mathrand.Intn(len(candidates))]
	gi, gj := pick.i, pick.j

	// Swap item gi (a→b) and item gj (b→a).
	a.Items[gi], b.Items[gi] = b.Items[gi], a.Items[gi]
	a.ItemNames[gi], b.ItemNames[gi] = b.ItemNames[gi], a.ItemNames[gi]
	a.Items[gj], b.Items[gj] = b.Items[gj], a.Items[gj]
	a.ItemNames[gj], b.ItemNames[gj] = b.ItemNames[gj], a.ItemNames[gj]

	// Describe what each player received (after the swap).
	descItem := func(p *Player, slot int) string {
		name := p.ItemNames[slot]
		if name != "" {
			return fmt.Sprintf("%s "+iI+"%s"+iI+" (lv%d)", articleFor(name), name, p.Items[slot])
		}
		return fmt.Sprintf("a lv%d "+iI+"%s"+iI, p.Items[slot], itemSlots[slot])
	}
	aGot := descItem(a, gj) // a received slot gj from b
	bGot := descItem(b, gi) // b received slot gi from a
	aName, bName := a.Name, b.Name
	g.mu.Unlock()

	g.say(fmt.Sprintf(iB+cCyan+"%s"+iC+iB+" gets %s; "+iB+cCyan+"%s"+iC+iB+" gets %s. Both walk away satisfied.",
		aName, aGot, bName, bGot))
	g.save()
}

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

	winner.BattlesWon++
	wName, lName := winner.Name, loser.Name
	wSum, lSum := winner.itemSum(), loser.itemSum()

	stealMsg := g.tryStealItem(winner, loser)
	battleAchMsgs := g.checkAchievements(winner)
	g.mu.Unlock()

	critNote := ""
	if crit {
		critNote = critNoteMsgs[mathrand.Intn(len(critNoteMsgs))]
	}
	g.say(fmt.Sprintf(battleMsgs[mathrand.Intn(len(battleMsgs))],
		wName, wRoll, wSum, lName, lRoll, lSum, critNote, pct))
	if stealMsg != "" {
		g.say(stealMsg)
	}
	for _, m := range battleAchMsgs {
		g.say(m)
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
		pct := mathrand.Intn(14) + 12 // 12–25%
		change := p.TTL * int64(pct) / 100
		if change < 1 {
			change = 1
		}
		p.TTL -= change
		if p.TTL < 1 {
			p.TTL = 1
		}
		return fmt.Sprintf(botBattleWinMsgs[mathrand.Intn(len(botBattleWinMsgs))],
			p.Name, pRoll, pSum, botRoll, botSum, pct)
	}

	pct := mathrand.Intn(11) + 5 // 5–15%
	change := p.TTL * int64(pct) / 100
	if change < 1 {
		change = 1
	}
	p.TTL += change
	return fmt.Sprintf(botBattleLossMsgs[mathrand.Intn(len(botBattleLossMsgs))],
		p.Name, pRoll, pSum, botRoll, botSum, pct)
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
		itemDesc = stolenName
	}
	if stolen > winner.Items[slot] {
		winner.Items[slot] = stolen
		winner.ItemNames[slot] = stolenName
		return fmt.Sprintf(stealEquipMsgs[mathrand.Intn(len(stealEquipMsgs))],
			winner.Name, loser.Name, itemDesc, stolen)
	}
	return fmt.Sprintf(stealDiscardMsgs[mathrand.Intn(len(stealDiscardMsgs))],
		winner.Name, loser.Name, itemDesc, stolen)
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

	switch mathrand.Intn(6) {
	case 0: // TTL calamity
		p.TTL += change
		return genderize(fmt.Sprintf(calamityMsgs[mathrand.Intn(len(calamityMsgs))], p.Name, pct)+
			fmt.Sprintf(" Next phase: "+iB+"%s"+iB+".", fmtDuration(p.TTL)), p)

	case 1: // TTL godsend
		p.TTL -= change
		if p.TTL < 1 {
			p.TTL = 1
		}
		return genderize(fmt.Sprintf(godsendMsgs[mathrand.Intn(len(godsendMsgs))], p.Name, pct)+
			fmt.Sprintf(" Next phase: "+iB+"%s"+iB+".", fmtDuration(p.TTL)), p)

	case 2: // Item calamity — degrade one non-zero slot
		slot := g.pickNonZeroSlot(p)
		if slot < 0 {
			// No items yet; fall back to a TTL calamity.
			p.TTL += change
			return genderize(fmt.Sprintf(calamityMsgs[0], p.Name, pct)+
				fmt.Sprintf(" Next phase: "+iB+"%s"+iB+".", fmtDuration(p.TTL)), p)
		}
		old := p.Items[slot]
		degraded := old * (100 - pct) / 100
		label := itemLabel(p, slot)
		if degraded <= 2 {
			p.Items[slot] = 0
			p.ItemNames[slot] = ""
			return genderize(fmt.Sprintf(itemDestroyedMsgs[mathrand.Intn(len(itemDestroyedMsgs))], p.Name, label), p)
		}
		p.Items[slot] = degraded
		return genderize(fmt.Sprintf(itemCalamityMsgs[mathrand.Intn(len(itemCalamityMsgs))], p.Name, label, pct), p)

	case 3: // Item godsend — improve one slot (creates a level-1 item if all empty)
		slot := g.pickNonZeroSlot(p)
		if slot < 0 {
			slot = mathrand.Intn(10)
			p.Items[slot] = 1
		}
		old := p.Items[slot]
		p.Items[slot] = int(math.Max(float64(old)*float64(100+pct)/100, float64(old)+1))
		return genderize(fmt.Sprintf(itemGodsendMsgs[mathrand.Intn(len(itemGodsendMsgs))], p.Name, itemLabel(p, slot), pct), p)

	case 4: // Found item — random slot, level up to 1.5× player level
		slot := mathrand.Intn(10)
		maxItem := int(math.Max(float64(p.Level)*1.5, 1))
		found := mathrand.Intn(maxItem) + 1
		slotName := itemSlots[slot]
		itemName := generateItemName(rarityNormal, slotName)
		currentName := p.ItemNames[slot]
		var currentLabel string
		if currentName == "" {
			currentLabel = p.their() + " " + slotName
		} else {
			currentLabel = p.their() + " " + currentName
		}
		equipped := "but it's worse than " + currentLabel
		if found > p.Items[slot] {
			p.Items[slot] = found
			p.ItemNames[slot] = itemName
			equipped = "and equips it"
		}
		return genderize(fmt.Sprintf(foundItemMsgs[mathrand.Intn(len(foundItemMsgs))],
			p.Name, articleFor(itemName), itemName, found, equipped, p.itemSum()), p)

	default: // Warp — teleport to a random position; range scales with level.
		warpRange := p.Level * 10
		if warpRange > 250 {
			warpRange = 250
		}
		if warpRange < 5 {
			warpRange = 5
		}
		dx := mathrand.Intn(2*warpRange+1) - warpRange
		dy := mathrand.Intn(2*warpRange+1) - warpRange
		p.X = (p.X + dx + gridSize*10) % gridSize
		p.Y = (p.Y + dy + gridSize*10) % gridSize
		return genderize(fmt.Sprintf(warpMsgs[mathrand.Intn(len(warpMsgs))], p.Name, p.X, p.Y), p)
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
// voidStorm hits all online players with a TTL penalty (8–20%) and has a 40%
// chance to degrade one random item slot per player by 5–10%. Must be called
// with mu held.
func (g *Game) voidStorm(online []*Player) []string {
	msgs := []string{eventHeader("🌩️", "VOID STORM"), voidStormOpenMsgs[mathrand.Intn(len(voidStormOpenMsgs))]}
	for _, p := range online {
		pct := mathrand.Intn(13) + 8 // 8–20%
		change := p.TTL * int64(pct) / 100
		if change < 1 {
			change = 1
		}
		p.TTL += change
		msgs = append(msgs, fmt.Sprintf(voidStormHitMsgs[mathrand.Intn(len(voidStormHitMsgs))], p.Name, pct))
		if mathrand.Intn(10) < 4 { // 40% item degradation
			slot := g.pickNonZeroSlot(p)
			if slot >= 0 {
				ipct := mathrand.Intn(6) + 5 // 5–10%
				old := p.Items[slot]
				degraded := old * (100 - ipct) / 100
				label := itemLabel(p, slot)
				if degraded <= 2 {
					p.Items[slot] = 0
					p.ItemNames[slot] = ""
					msgs = append(msgs, genderize(fmt.Sprintf(itemDestroyedMsgs[mathrand.Intn(len(itemDestroyedMsgs))], p.Name, label), p))
				} else {
					p.Items[slot] = degraded
					msgs = append(msgs, fmt.Sprintf(iB+cCyan+"%s"+iC+iB+"'s "+iI+"%s"+iI+" is damaged by the surge (%d%% degraded).",
						p.Name, label, ipct))
				}
			}
		}
	}
	return msgs
}

// 80% chance to help (5–75% TTL reduction), 20% chance to hurt (same range).
// Must be called with mu held.
func (g *Game) handOfGod(p *Player) []string {
	pct := mathrand.Intn(71) + 5 // 5–75%
	change := p.TTL * int64(pct) / 100
	if change < 1 {
		change = 1
	}
	header := eventHeader("✋", "HAND OF GOD")
	if mathrand.Intn(5) == 0 { // 20% hurt
		p.TTL += change
		return []string{header, fmt.Sprintf(handOfGodMsgs[0][mathrand.Intn(len(handOfGodMsgs[0]))], p.Name, pct)}
	}
	p.TTL -= change
	if p.TTL < 1 {
		p.TTL = 1
	}
	return []string{header, fmt.Sprintf(handOfGodMsgs[1][mathrand.Intn(len(handOfGodMsgs[1]))], p.Name, pct)}
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

	winPct := int64(mathrand.Intn(14) + 12) // 12–25%
	losPct := int64(mathrand.Intn(11) + 5)  // 5–15%

	// Scale TTL change to the weakest winner so no single player is wiped out.
	minWinnerTTL := winners[0].TTL
	for _, p := range winners[1:] {
		if p.TTL < minWinnerTTL {
			minWinnerTTL = p.TTL
		}
	}
	change := minWinnerTTL * winPct / 100

	for _, p := range winners {
		p.TTL -= change
		if p.TTL < 0 {
			p.TTL = 0
		}
	}
	for _, p := range losers {
		lchange := p.TTL * losPct / 100
		p.TTL += lchange
	}

	names := func(team []*Player) string {
		ns := make([]string, len(team))
		for i, p := range team {
			ns[i] = p.Name
		}
		return strings.Join(ns, ", ")
	}

	return []string{
		eventHeader("⚔️", "TEAM BATTLE"),
		fmt.Sprintf(teamBattleOpenMsgs[mathrand.Intn(len(teamBattleOpenMsgs))],
			names(winners), wSum, names(losers), lSum, wRoll, lRoll),
		fmt.Sprintf(teamBattleWinMsgs[mathrand.Intn(len(teamBattleWinMsgs))], names(winners), winPct),
	}
}

// tryStartQuest attempts to launch a quest when conditions are met: at least
// questMinPlayers players at questMinLevel+ are online. Randomly chooses
// between a grid quest (reach coordinates) and a time quest (stay online).
// Must be called with mu held.
func (g *Game) tryStartQuest(online []*Player) []string {
	minLevel := questMinLevel
	minPlayers := questMinPlayers
	if g.DevMode {
		minLevel = 0
		minPlayers = 1
	}
	eligible := make([]*Player, 0)
	for _, p := range online {
		if p.Level >= minLevel {
			eligible = append(eligible, p)
		}
	}
	if len(eligible) < minPlayers {
		return nil
	}

	mathrand.Shuffle(len(eligible), func(i, j int) { eligible[i], eligible[j] = eligible[j], eligible[i] })
	if len(eligible) > questMinPlayers {
		eligible = eligible[:questMinPlayers]
	}
	questers := eligible

	desc := questDescs[mathrand.Intn(len(questDescs))]
	duration := time.Duration(mathrand.Intn(3)+1) * time.Hour // 1–3 hours

	names := make([]string, len(questers))
	for i, p := range questers {
		names[i] = p.Name
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
		gridStarts := []string{
			"🗺 Grid mission: " + iB + "%s" + iB + " must converge on (" + iB + "%d,%d" + iB + ") to " + iI + "%s" + iI + ". Window: " + iB + "%s" + iB + ".",
			"Navigation alert — " + iB + "%s" + iB + ": reach (" + iB + "%d,%d" + iB + ") and " + iI + "%s" + iI + ". Time remaining: " + iB + "%s" + iB + ".",
			"Coordinate lock: " + iB + "%s" + iB + " — objective (" + iB + "%d,%d" + iB + "): " + iI + "%s" + iI + ". You have " + iB + "%s" + iB + ".",
		}
		return []string{
			eventHeader("🌀", "QUEST"),
			fmt.Sprintf(gridStarts[mathrand.Intn(len(gridStarts))],
				strings.Join(names, ", "), qx, qy, desc, fmtDuration(int64(duration.Seconds()))),
		}
	}

	g.quest = &Quest{
		Questers:      questers,
		EndsAt:        time.Now().Add(duration),
		Desc:          desc,
		OnlineAtStart: onlineAtStart,
	}
	timeStarts := []string{
		"⚡ Mission alert — " + iB + "%s" + iB + " have been tasked to " + iI + "%s" + iI + ". Window: " + iB + "%s" + iB + ". Do not fail.",
		"Deployment: " + iB + "%s" + iB + " — objective: " + iI + "%s" + iI + ". Time remaining: " + iB + "%s" + iB + ".",
		"The call goes out to " + iB + "%s" + iB + ": " + iI + "%s" + iI + ". You have " + iB + "%s" + iB + ".",
	}
	return []string{
		eventHeader("🌀", "QUEST"),
		fmt.Sprintf(timeStarts[mathrand.Intn(len(timeStarts))],
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
		names[i] = p.Name
	}

	questers := strings.Join(names, ", ")

	if allOnline {
		questPct := mathrand.Intn(11) + 20 // 20–30%
		var questAchMsgs []string
		for _, qp := range quest.Questers {
			change := qp.TTL * int64(questPct) / 100
			qp.TTL -= change
			if qp.TTL < 1 {
				qp.TTL = 1
			}
			qp.QuestsCompleted++
			questAchMsgs = append(questAchMsgs, g.checkAchievements(qp)...)
		}
		if quest.IsGrid {
			g.lastEvent = fmt.Sprintf("✔ Quest: %s completed %q at (%d,%d) — phase +%d%%", questers, quest.Desc, quest.QX, quest.QY, questPct)
			gridSuccess := []string{
				"✔ Grid mission complete. " + iB + "%s" + iB + " converged on (" + iB + "%d,%d" + iB + ") and " + iI + "%s" + iI + ". Phase advanced by " + iB + cTeal + "%d%%" + iC + iB + ".",
				iB + "%s" + iB + " reached (" + iB + "%d,%d" + iB + ") — objective met: " + iI + "%s" + iI + ". Phase advanced by " + iB + cTeal + "%d%%" + iC + iB + ".",
				"All questers at (" + iB + "%d,%d" + iB + "). " + iB + "%s" + iB + " completed their mission to " + iI + "%s" + iI + ". Phase advanced by " + iB + cTeal + "%d%%" + iC + iB + ".",
			}
			idx := mathrand.Intn(len(gridSuccess))
			var msg string
			if idx == 2 {
				msg = fmt.Sprintf(gridSuccess[idx], quest.QX, quest.QY, questers, quest.Desc, questPct)
			} else {
				msg = fmt.Sprintf(gridSuccess[idx], questers, quest.QX, quest.QY, quest.Desc, questPct)
			}
			return append([]string{eventHeader("✅", "QUEST COMPLETE"), msg}, questAchMsgs...)
		}
		g.lastEvent = fmt.Sprintf("✔ Quest: %s completed %q — phase +%d%%", questers, quest.Desc, questPct)
		timeSuccess := []string{
			"✔ Mission complete. " + iB + "%s" + iB + " succeeded in their objective to " + iI + "%s" + iI + ". Phase advanced by " + iB + cTeal + "%d%%" + iC + iB + ".",
			iB + "%s" + iB + " return from the mission to " + iI + "%s" + iI + ". Against expectations, they made it. Phase advanced by " + iB + cTeal + "%d%%" + iC + iB + ".",
			"Confirmed: " + iB + "%s" + iB + " completed the objective — " + iI + "%s" + iI + ". Phase advanced by " + iB + cTeal + "%d%%" + iC + iB + ".",
		}
		return append([]string{
			eventHeader("✅", "QUEST COMPLETE"),
			fmt.Sprintf(timeSuccess[mathrand.Intn(len(timeSuccess))], questers, quest.Desc, questPct),
		}, questAchMsgs...)
	}

	for _, p := range online {
		if quest.OnlineAtStart[strings.ToLower(p.Nick)] {
			g.applyPenalty(p, 15, penQuest)
		}
	}
	if quest.IsGrid {
		reached := make([]string, 0, len(quest.Reached))
		for nick := range quest.Reached {
			reached = append(reached, nick)
		}
		suffix := "none reached the coordinates"
		if len(reached) > 0 {
			suffix = fmt.Sprintf("only %s made it to (%d,%d)", strings.Join(reached, ", "), quest.QX, quest.QY)
		}
		g.lastEvent = fmt.Sprintf("✘ Quest failed: %s — %s. Penalty p15", questers, suffix)
		gridFail := []string{
			"✘ Grid mission failed. " + iB + "%s" + iB + " did not all reach (" + iB + "%d,%d" + iB + ") to " + iI + "%s" + iI + " (%s). Everyone present suffers.",
			"The rendezvous at (" + iB + "%d,%d" + iB + ") never happened. " + iB + "%s" + iB + " failed to " + iI + "%s" + iI + " (%s). Penalty for all.",
		}
		idx := mathrand.Intn(len(gridFail))
		header := eventHeader("❌", "QUEST FAILED")
		if idx == 1 {
			return []string{header, fmt.Sprintf(gridFail[idx], quest.QX, quest.QY, questers, quest.Desc, suffix)}
		}
		return []string{header, fmt.Sprintf(gridFail[idx], questers, quest.QX, quest.QY, quest.Desc, suffix)}
	}
	g.lastEvent = fmt.Sprintf("✘ Quest failed: %s — %q. Penalty p15", questers, quest.Desc)
	timeFail := []string{
		"✘ Mission failed. " + iB + "%s" + iB + " did not complete: " + iI + "%s" + iI + ". All present suffer a penalty.",
		iB + "%s" + iB + " abandoned the mission to " + iI + "%s" + iI + ". The consequences fall on everyone still here.",
		"The objective — " + iI + "%s" + iI + " — is lost. " + iB + "%s" + iB + " did not hold. Everyone pays.",
	}
	idx := mathrand.Intn(len(timeFail))
	header := eventHeader("❌", "QUEST FAILED")
	if idx == 2 {
		return []string{header, fmt.Sprintf(timeFail[idx], quest.Desc, questers)}
	}
	return []string{header, fmt.Sprintf(timeFail[idx], questers, quest.Desc)}
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
	return fmt.Sprintf(goodEventMsgs[mathrand.Intn(len(goodEventMsgs))], p.Name, partner.Name, pct)
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
			stolenName := target.ItemNames[slot]
			label := itemLabel(target, slot)
			target.Items[slot] = 0
			target.ItemNames[slot] = ""
			if stolen > p.Items[slot] {
				p.Items[slot] = stolen
				p.ItemNames[slot] = stolenName
			}
			return fmt.Sprintf(evilStealMsgs[mathrand.Intn(len(evilStealMsgs))],
				p.Name, target.Name, label, stolen)
		}
	}

	// Forsaken: dark patron punishes the evil player.
	pct := mathrand.Intn(5) + 1
	change := p.TTL * int64(pct) / 100
	if change < 1 {
		change = 1
	}
	p.TTL += change
	return fmt.Sprintf(forsakenMsgs[mathrand.Intn(len(forsakenMsgs))], p.Name, pct)
}

// Penalty kind constants used by applyPenalty to route to the right counter.
const (
	penMesg  = "mesg"
	penNick  = "nick"
	penPart  = "part"
	penKick  = "kick"
	penQuit  = "quit"
	penQuest = "quest"
	penOther = "other"
)

// applyPenalty adds base × 1.14^level seconds to p's TTL and records the
// amount in the matching per-source counter. Must be called with mu held.
func (g *Game) applyPenalty(p *Player, base int64, kind string) {
	delta := int64(float64(base) * math.Pow(1.14, float64(p.Level)))
	p.TTL += delta
	switch kind {
	case penMesg:
		p.PenMesg += delta
	case penNick:
		p.PenNick += delta
	case penPart:
		p.PenPart += delta
	case penKick:
		p.PenKick += delta
	case penQuit:
		p.PenQuit += delta
	case penQuest:
		p.PenQuest += delta
	default:
		p.PenOther += delta
	}
}

// penTotal returns the sum of all per-source penalty counters for p.
func (p *Player) penTotal() int64 {
	return p.PenMesg + p.PenNick + p.PenPart + p.PenKick + p.PenQuit + p.PenQuest + p.PenOther
}

// sortedPlayers returns all players sorted by level desc, TTL asc — the same
// order used by !all. Safe to call without mu held (acquires it internally).
func (g *Game) sortedPlayers() []*Player {
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
	return players
}

// findPlayer resolves a query to a player using three strategies in order:
//  1. IRC nick (map key lookup)
//  2. character name (case-insensitive)
//  3. !all index (numeric string, 1-based)
//
// Returns nil if no player matches. Safe to call without mu held.
func (g *Game) findPlayer(query string) *Player {
	lower := strings.ToLower(query)
	g.mu.Lock()
	// 1. IRC nick
	if p, ok := g.players[lower]; ok {
		g.mu.Unlock()
		return p
	}
	// 2. character name
	for _, p := range g.players {
		if strings.ToLower(p.Name) == lower {
			g.mu.Unlock()
			return p
		}
	}
	g.mu.Unlock()
	// 3. !all index
	if idx, err := strconv.Atoi(query); err == nil && idx >= 1 {
		sorted := g.sortedPlayers()
		if idx <= len(sorted) {
			return sorted[idx-1]
		}
	}
	return nil
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
		// Migration: players registered before the Name field was added have
		// an empty Name; fall back to their IRC nick as the character name.
		if p.Name == "" {
			p.Name = p.Nick
		}
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
		t /= 14
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
	iI + "The Drift is quiet. For now." + iI,
	iI + "Silence in the void — idle and endure." + iI,
	iI + "The Pale Architects do not reward haste." + iI,
	iI + "The Signal waits for new carriers." + iI,
	iI + "Patience is the only armour the Null respects." + iI,
	iI + "Even the Entities began by doing nothing." + iI,
}

// topicRateLimit is the minimum interval between routine topic updates.
// Notable events (noteEvent) bypass this and always push immediately.
const topicRateLimit = 5 * time.Minute

// updateTopic rebuilds and sets the channel topic, but only if the rate limit
// has elapsed since the last push. Use noteEvent for important updates that
// must always push. Must NOT be called while holding mu.
func (g *Game) updateTopic() {
	if g.setTopic == nil {
		return
	}
	g.mu.Lock()
	if time.Since(g.lastTopicSet) < topicRateLimit {
		g.mu.Unlock()
		return
	}
	topic := g.buildTopic()
	g.lastTopicSet = time.Now()
	g.mu.Unlock()
	g.setTopic(topic)
}

// pushTopic unconditionally rebuilds and sets the channel topic, bypassing
// the rate limit. Use for notable events that warrant an immediate update.
// Must NOT be called while holding mu.
func (g *Game) pushTopic() {
	if g.setTopic == nil {
		return
	}
	g.mu.Lock()
	topic := g.buildTopic()
	g.lastTopicSet = time.Now()
	g.mu.Unlock()
	g.setTopic(topic)
}

// buildTopic assembles the channel topic: at most three parts.
//   🌀 Void Drift | N/M idling | <quest or last notable event>
//
// Must be called with mu held.
func (g *Game) buildTopic() string {
	online, total := 0, len(g.players)
	for _, p := range g.players {
		if p.Online {
			online++
		}
	}

	parts := []string{iB + "🌀 Void Drift" + iB}
	if online == 0 && total == 0 {
		return strings.Join(append(parts, idleFlavors[mathrand.Intn(len(idleFlavors))]), " | ")
	}

	parts = append(parts, fmt.Sprintf(iB+"%d"+iB+"/"+iB+"%d"+iB+" idling", online, total))

	// Third part: active quest takes priority; fall back to last notable event;
	// fall back to idle flavour when nobody is online.
	if qp := g.questTopicPart(); qp != "" {
		parts = append(parts, qp)
	} else if g.lastEvent != "" {
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
	names := make([]string, len(g.quest.Questers))
	for i, qp := range g.quest.Questers {
		names[i] = qp.Name
	}
	questers := strings.Join(names, ", ")
	if g.quest.IsGrid {
		arrived := len(g.quest.Reached)
		total := len(g.quest.Questers)
		return fmt.Sprintf("🗺 Quest: "+iB+"%s"+iB+" → ("+iB+"%d,%d"+iB+") "+iI+"%s"+iI+" ["+iB+"%d/%d"+iB+" arrived, "+iB+"%s"+iB+"]",
			questers, g.quest.QX, g.quest.QY, g.quest.Desc, arrived, total, remaining)
	}
	return fmt.Sprintf("⚡ Quest: "+iB+"%s"+iB+" — "+iI+"%s"+iI+" ["+iB+"%s"+iB+" remaining]",
		questers, g.quest.Desc, remaining)
}

// eventHeader returns a bold, emoji-framed header line for major server events.
func eventHeader(emoji, label string) string {
	return iB + emoji + " 〔 " + label + " 〕 " + emoji + iB
}

// noteEvent records msg as the most recent notable event and immediately
// pushes the topic (bypassing the rate limit). Use for events worth showing:
// legendary drops, rare level-ups, quest starts/completions.
// Must NOT be called while holding mu.
func (g *Game) noteEvent(msg string) {
	g.mu.Lock()
	g.lastEvent = msg
	g.mu.Unlock()
	g.pushTopic()
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
// itemSum is augmented by the focus-slot item level (counted an extra time).
func effectiveItemSum(p *Player) int {
	return p.itemSum() + p.Items[classFocusSlot(p.Class)]
}

// fmtDuration formats a duration given in seconds as a human-readable string
// in the form "Xh MM m SS s", "MM m SS s", or "SS s".
// articleFor returns "an" if word starts with a vowel, "a" otherwise.
func articleFor(word string) string {
	if len(word) > 0 && strings.ContainsRune("aeiouAEIOU", rune(word[0])) {
		return "an"
	}
	return "a"
}

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
