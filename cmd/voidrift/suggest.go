// suggest.go generates character name and class suggestions for unregistered
// players who join the channel, using themed wordlists that match the
// cosmic horror / sci-fi setting of Void Drift.
package main

import (
	mathrand "math/rand"
	"strings"
)

var suggestGivenNames = []string{
	"Vael", "Keth", "Sora", "Orin", "Drex", "Mira", "Cael", "Rhen",
	"Tova", "Lyx", "Neth", "Sael", "Vorin", "Kira", "Reth", "Omyr",
	"Lyra", "Caen", "Asha", "Threk", "Hesh", "Vel", "Dael", "Kael",
	"Aen", "Syx", "Orm", "Vreth", "Solen", "Taen", "Zara", "Prae",
	"Drae", "Mael", "Kern", "Sive", "Orath", "Lyss", "Vaen", "Rhyn",
}

var suggestEpithets = []string{
	"Ashborne", "Voidborn", "Driftmark", "Nullscar", "Palehand",
	"Echoless", "Signalless", "Collapseborn", "Veilmarked", "Driftbound",
	"Phasescar", "Entropyborn", "Echomarked", "Voidwalker", "Nullborn",
	"Ashwalker", "Veilborn", "Driftmarked", "Ashscar", "Signalborn",
	"Paleeye", "Nullwalker", "Voidmark", "Driftscar", "Echoborn",
}

var suggestClasses = []string{
	"NullWalker", "DriftSeeker", "SignalGhost", "VoidTouched",
	"PhaseDrifter", "EchoRemnant", "EntropySinger", "PaleArchitect",
	"VeilRunner", "CollapseSurvivor", "SignalHunter", "VoidDrifter",
	"NullSeeker", "DriftPhantom", "PhaseGhost", "EchoWalker",
	"ArchitectsShade", "DriftHermit", "NullAcolyte", "SignalWraith",
}

// generateSuggestion returns a random (name, class) pair drawn from the themed
// wordlists. Both are single IRC tokens (no spaces) suitable for use directly
// in !register: name is GivenNameEpithet (CamelCase, no separator), class is CamelCase.
// takenNames is a set of lowercase names already in use; the function retries
// until it finds a free name or exhausts all combinations.
func generateSuggestion(takenNames map[string]struct{}) (name, class string) {
	// Shuffle indices to avoid clustering on the same given names.
	givens := mathrand.Perm(len(suggestGivenNames))
	epithets := mathrand.Perm(len(suggestEpithets))
	for _, gi := range givens {
		for _, ei := range epithets {
			candidate := suggestGivenNames[gi] + suggestEpithets[ei]
			if _, taken := takenNames[strings.ToLower(candidate)]; !taken {
				name = candidate
				class = suggestClasses[mathrand.Intn(len(suggestClasses))]
				return
			}
		}
	}
	// All combinations taken — fall back to any name.
	name = suggestGivenNames[mathrand.Intn(len(suggestGivenNames))] + suggestEpithets[mathrand.Intn(len(suggestEpithets))]
	class = suggestClasses[mathrand.Intn(len(suggestClasses))]
	return
}
