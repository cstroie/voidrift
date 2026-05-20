// suggest.go generates character name and class suggestions for unregistered
// players who join the channel, using themed wordlists that match the
// cosmic horror / sci-fi setting of Void Drift.
package main

import mathrand "math/rand"

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
	"Null-Walker", "Drift-Seeker", "Signal-Ghost", "Void-Touched",
	"Phase-Drifter", "Echo-Remnant", "Entropy-Singer", "Pale-Architect",
	"Veil-Runner", "Collapse-Survivor", "Signal-Hunter", "Void-Drifter",
	"Null-Seeker", "Drift-Phantom", "Phase-Ghost", "Echo-Walker",
	"Architects-Shade", "Drift-Hermit", "Null-Acolyte", "Signal-Wraith",
}

// generateSuggestion returns a random (name, class) pair drawn from the themed
// wordlists. Both are single IRC tokens (no spaces) suitable for use directly
// in !register: name is GivenName-Epithet, class is Hyphenated-Class.
func generateSuggestion() (name, class string) {
	given := suggestGivenNames[mathrand.Intn(len(suggestGivenNames))]
	epithet := suggestEpithets[mathrand.Intn(len(suggestEpithets))]
	name = given + "-" + epithet
	class = suggestClasses[mathrand.Intn(len(suggestClasses))]
	return
}
