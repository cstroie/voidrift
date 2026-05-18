package main

import (
	"fmt"
	"strings"
	"testing"
)

// newTestGame returns a Game with no data files and a no-op say/setTopic,
// suitable for unit tests that do not need IRC or disk I/O.
func newTestGame() *Game {
	g := &Game{
		players: make(map[string]*Player),
		guilds:  make(map[string]*Guild),
		say:     func(string) {},
		Rates:   defaultRates(),
	}
	g.setTopic = func(string) {}
	return g
}

// addPlayer inserts a player directly into the game without using CmdRegister,
// so tests can set arbitrary initial state (level, TTL, items, alignment, etc.).
func addPlayer(g *Game, nick, class string) *Player {
	p := &Player{
		Nick:  nick,
		Class: class,
		TTL:   g.ttlForLevel(0),
	}
	g.players[strings.ToLower(nick)] = p
	return p
}

// --- fmtDuration ---

func TestFmtDuration(t *testing.T) {
	cases := []struct {
		secs int64
		want string
	}{
		{0, "0s"},
		{-5, "0s"},
		{45, "45s"},
		{60, "1m00s"},
		{61, "1m01s"},
		{3600, "1h00m00s"},
		{3661, "1h01m01s"},
		{86400, "24h00m00s"},
	}
	for _, c := range cases {
		got := fmtDuration(c.secs)
		if got != c.want {
			t.Errorf("fmtDuration(%d) = %q, want %q", c.secs, got, c.want)
		}
	}
}

// --- extractNick ---

func TestExtractNick(t *testing.T) {
	cases := []struct{ src, want string }{
		{"Alice!alice@host.net", "Alice"},
		{"Bob", "Bob"},
		{"!noNick", "!noNick"}, // no '!' at positive index
		{"a!b@c", "a"},
	}
	for _, c := range cases {
		got := extractNick(c.src)
		if got != c.want {
			t.Errorf("extractNick(%q) = %q, want %q", c.src, got, c.want)
		}
	}
}

// --- isChannel ---

func TestIsChannel(t *testing.T) {
	yes := []string{"#general", "&local", "!service", "+moderated"}
	no := []string{"Alice", "", "goirc", "123"}
	for _, s := range yes {
		if !isChannel(s) {
			t.Errorf("isChannel(%q) should be true", s)
		}
	}
	for _, s := range no {
		if isChannel(s) {
			t.Errorf("isChannel(%q) should be false", s)
		}
	}
}

// --- hashPass / newSalt ---

func TestHashPassDeterministic(t *testing.T) {
	salt := "0102030405060708090a0b0c0d0e0f10"
	h1 := hashPass(salt, "secret")
	h2 := hashPass(salt, "secret")
	if h1 != h2 {
		t.Fatal("hashPass is not deterministic")
	}
	if hashPass(salt, "secret") == hashPass(salt, "other") {
		t.Fatal("different passwords produced the same hash")
	}
}

func TestNewSaltLength(t *testing.T) {
	s := newSalt()
	if len(s) != 32 {
		t.Fatalf("newSalt length = %d, want 32", len(s))
	}
	s2 := newSalt()
	if s == s2 {
		t.Fatal("two calls to newSalt returned identical salts")
	}
}

// --- ttlForLevel ---

func TestTTLForLevel(t *testing.T) {
	g := newTestGame()

	// Level 0: 600 * 1.16^0 = 600
	if got := g.ttlForLevel(0); got != 600 {
		t.Errorf("ttlForLevel(0) = %d, want 600", got)
	}

	// Each level should be strictly longer than the previous.
	prev := g.ttlForLevel(0)
	for lvl := 1; lvl <= 70; lvl++ {
		cur := g.ttlForLevel(lvl)
		if cur <= prev {
			t.Errorf("ttlForLevel(%d)=%d should be > ttlForLevel(%d)=%d", lvl, cur, lvl-1, prev)
		}
		prev = cur
	}

	// Level 61 = base_60 + 86400*1; must be more than one day beyond level 60.
	diff := g.ttlForLevel(61) - g.ttlForLevel(60)
	if diff != 86400 {
		t.Errorf("ttlForLevel(61)-ttlForLevel(60) = %d, want 86400", diff)
	}

	// DevMode divides by 5.
	gd := newTestGame()
	gd.DevMode = true
	for _, lvl := range []int{0, 10, 60, 65} {
		normal := g.ttlForLevel(lvl)
		dev := gd.ttlForLevel(lvl)
		if dev != normal/5 {
			t.Errorf("DevMode ttlForLevel(%d) = %d, want %d", lvl, dev, normal/5)
		}
	}
}

// --- applyPenalty ---

func TestApplyPenalty(t *testing.T) {
	g := newTestGame()
	p := &Player{Level: 0, TTL: 1000}

	// At level 0: penalty = base * 1.14^0 = base * 1 = base.
	g.applyPenalty(p, 100)
	if p.TTL != 1100 {
		t.Errorf("applyPenalty level 0: TTL = %d, want 1100", p.TTL)
	}

	// At level 10: multiplier = 1.14^10 ≈ 3.707; base 100 → ~370.
	p2 := &Player{Level: 10, TTL: 0}
	g.applyPenalty(p2, 100)
	if p2.TTL < 350 || p2.TTL > 400 {
		t.Errorf("applyPenalty level 10, base 100: TTL = %d, want ~370", p2.TTL)
	}

	// Penalty always increases TTL.
	p3 := &Player{Level: 5, TTL: 500}
	before := p3.TTL
	g.applyPenalty(p3, 1)
	if p3.TTL <= before {
		t.Errorf("applyPenalty should increase TTL")
	}
}

// --- classFocusSlot ---

func TestClassFocusSlot(t *testing.T) {
	// Must return a value in [0, 9].
	classes := []string{"Warrior", "Mage", "Rogue", "Paladin", "Necromancer", ""}
	for _, c := range classes {
		slot := classFocusSlot(c)
		if slot < 0 || slot > 9 {
			t.Errorf("classFocusSlot(%q) = %d, want 0–9", c, slot)
		}
	}

	// Case-insensitive: "warrior" and "WARRIOR" must map to the same slot.
	if classFocusSlot("warrior") != classFocusSlot("WARRIOR") {
		t.Error("classFocusSlot is not case-insensitive")
	}

	// Deterministic across calls.
	for i := 0; i < 10; i++ {
		if classFocusSlot("Mage") != classFocusSlot("Mage") {
			t.Error("classFocusSlot is not deterministic")
		}
	}
}

// --- itemSum / effectiveItemSum ---

func TestItemSum(t *testing.T) {
	p := &Player{}
	if p.itemSum() != 0 {
		t.Error("itemSum of empty player should be 0")
	}
	for i := range p.Items {
		p.Items[i] = i + 1
	}
	// 1+2+…+10 = 55
	if got := p.itemSum(); got != 55 {
		t.Errorf("itemSum = %d, want 55", got)
	}
}

func TestEffectiveItemSum(t *testing.T) {
	p := &Player{Class: "Warrior"}
	for i := range p.Items {
		p.Items[i] = 10
	}
	// effectiveItemSum = itemSum + Items[focus] = 100 + 10 = 110
	// (focus slot value is 10 regardless of which slot it is)
	want := 110
	if got := effectiveItemSum(p); got != want {
		t.Errorf("effectiveItemSum (single class) = %d, want %d", got, want)
	}

	// Dual-class always adds a second focus bonus of 10, whether or not
	// the two focus slots are the same.
	p.Class2 = "Mage"
	wantDual := 120
	if got := effectiveItemSum(p); got != wantDual {
		t.Errorf("effectiveItemSum (dual-class) = %d, want %d", got, wantDual)
	}
}

// --- CmdRegister ---

func TestCmdRegisterBasic(t *testing.T) {
	g := newTestGame()
	msg := g.CmdRegister("Alice!a@h", "Warrior", "pass123")
	if !strings.Contains(msg, "Alice") || !strings.Contains(msg, "registered") {
		t.Errorf("unexpected register message: %q", msg)
	}
	if _, ok := g.players["alice"]; !ok {
		t.Error("player not inserted into map")
	}
}

func TestCmdRegisterUsesIRCNick(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	p, ok := g.players["alice"]
	if !ok {
		t.Fatal("player not found")
	}
	if p.Nick != "Alice" {
		t.Errorf("Nick = %q, want Alice (from IRC src)", p.Nick)
	}
}

func TestCmdRegisterDuplicate(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	msg := g.CmdRegister("Alice!a@h", "Mage", "pass2")
	if !strings.Contains(msg, "already registered") {
		t.Errorf("expected duplicate-nick error, got %q", msg)
	}
}

func TestCmdRegisterValidation(t *testing.T) {
	g := newTestGame()
	if msg := g.CmdRegister("A!a@h", "", "p"); !strings.Contains(msg, "Class") {
		t.Errorf("empty class: %q", msg)
	}
	longClass := strings.Repeat("x", 51)
	if msg := g.CmdRegister("A!a@h", longClass, "p"); !strings.Contains(msg, "Class") {
		t.Errorf("long class: %q", msg)
	}
}

// --- CmdLogin ---

func TestCmdLoginSuccess(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "secret")
	msg := g.CmdLogin("Alice!a@h", "secret")
	if !strings.Contains(msg, "logged in") {
		t.Errorf("expected logged-in confirmation, got %q", msg)
	}
	if !g.players["alice"].Online {
		t.Error("player should be online after login")
	}
}

func TestCmdLoginWrongPassword(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "secret")
	msg := g.CmdLogin("Alice!a@h", "wrong")
	if !strings.Contains(msg, "Wrong password") {
		t.Errorf("expected wrong-password error, got %q", msg)
	}
}

func TestCmdLoginUnknownNick(t *testing.T) {
	g := newTestGame()
	msg := g.CmdLogin("Nobody!n@h", "pass")
	if !strings.Contains(msg, "No character") {
		t.Errorf("expected no-character error, got %q", msg)
	}
}

// --- CmdLogout ---

func TestCmdLogout(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	msg := g.CmdLogout("Alice!a@h")
	if !strings.Contains(msg, "logged out") {
		t.Errorf("expected logged-out message, got %q", msg)
	}
	if g.players["alice"].Online {
		t.Error("player should be offline after logout")
	}
}

func TestCmdLogoutNotLoggedIn(t *testing.T) {
	g := newTestGame()
	msg := g.CmdLogout("Nobody!n@h")
	if !strings.Contains(msg, "not logged in") {
		t.Errorf("expected not-logged-in error, got %q", msg)
	}
}

// --- CmdAlign ---

func TestCmdAlign(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	p := g.players["alice"]
	origTTL := p.TTL

	// Changing alignment should apply a penalty.
	msg := g.CmdAlign("Alice!a@h", "good")
	if !strings.Contains(msg, "now good") {
		t.Errorf("expected alignment-change message, got %q", msg)
	}
	if p.TTL <= origTTL {
		t.Error("changing alignment should increase TTL")
	}
	if p.Alignment != AlignGood {
		t.Errorf("alignment = %d, want AlignGood", p.Alignment)
	}

	// Confirming the same alignment should NOT add a penalty.
	ttlAfterChange := p.TTL
	msg2 := g.CmdAlign("Alice!a@h", "good")
	if !strings.Contains(msg2, "already good") {
		t.Errorf("expected already-good message, got %q", msg2)
	}
	if p.TTL != ttlAfterChange {
		t.Error("confirming same alignment should not change TTL")
	}
}

func TestCmdAlignInvalid(t *testing.T) {
	g := newTestGame()
	msg := g.CmdAlign("X!x@h", "chaotic")
	if !strings.Contains(msg, "Usage") {
		t.Errorf("expected usage error, got %q", msg)
	}
}

// --- CmdDualClass ---

func TestCmdDualClass(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	p := g.players["alice"]

	// Below level 12: should fail.
	p.Level = 11
	msg := g.CmdDualClass("Alice!a@h", "Mage")
	if !strings.Contains(msg, "level 12") {
		t.Errorf("expected level-12 requirement, got %q", msg)
	}

	// At level 12: should succeed.
	p.Level = 12
	msg = g.CmdDualClass("Alice!a@h", "Mage")
	if !strings.Contains(msg, "Warrior/Mage") {
		t.Errorf("expected dual-class confirmation, got %q", msg)
	}
	if p.Class2 != "Mage" {
		t.Errorf("Class2 = %q, want Mage", p.Class2)
	}

	// Second call should fail.
	msg2 := g.CmdDualClass("Alice!a@h", "Rogue")
	if !strings.Contains(msg2, "already dual-classed") {
		t.Errorf("expected already-dual-classed error, got %q", msg2)
	}
}

// --- CmdStatus ---

func TestCmdStatus(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")

	msg := g.CmdStatus("Alice!a@h", "")
	if !strings.Contains(msg, "Alice") || !strings.Contains(msg, "Warrior") {
		t.Errorf("unexpected status: %q", msg)
	}

	// Target nick lookup.
	msg2 := g.CmdStatus("Bob!b@h", "Alice")
	if !strings.Contains(msg2, "Alice") {
		t.Errorf("status by nick: %q", msg2)
	}

	// Unknown nick.
	msg3 := g.CmdStatus("Bob!b@h", "Ghost")
	if !strings.Contains(msg3, "No character") {
		t.Errorf("unknown nick: %q", msg3)
	}
}

// --- CmdTop ---

func TestCmdTop(t *testing.T) {
	g := newTestGame()
	if msg := g.CmdTop(); !strings.Contains(msg, "No players") {
		t.Errorf("empty top: %q", msg)
	}

	for i := 1; i <= 7; i++ {
		nick := fmt.Sprintf("Player%d", i)
		p := addPlayer(g, nick, "Warrior")
		p.Level = i
		p.Online = true
	}

	msg := g.CmdTop()
	if !strings.Contains(msg, "Top players") {
		t.Errorf("unexpected top message: %q", msg)
	}
	// Player7 is highest level; must appear first.
	if !strings.Contains(msg, "Player7") {
		t.Errorf("top player not in result: %q", msg)
	}
	// At most 5 entries.
	count := strings.Count(msg, "lvl")
	if count > 5 {
		t.Errorf("top returned %d entries, want ≤5", count)
	}
}

// --- CmdOnline ---

func TestCmdOnline(t *testing.T) {
	g := newTestGame()
	if msg := g.CmdOnline(); !strings.Contains(msg, "No players") {
		t.Errorf("empty online: %q", msg)
	}

	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	msg := g.CmdOnline()
	if !strings.Contains(msg, "Alice") {
		t.Errorf("online list: %q", msg)
	}
	if !strings.Contains(msg, "Online (1)") {
		t.Errorf("online count: %q", msg)
	}
}

// --- OnJoin / OnPart / OnQuit / OnKick / OnNick ---

func TestOnJoin(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.OnJoin("Alice!a@host")
	if !g.players["alice"].Online {
		t.Error("player should be online after OnJoin")
	}
}

func TestOnPart(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	p := g.players["alice"]
	ttlBefore := p.TTL

	g.OnPart("Alice!a@h")
	if p.Online {
		t.Error("player should be offline after OnPart")
	}
	if p.TTL <= ttlBefore {
		t.Error("OnPart should apply penalty")
	}
}

func TestOnQuit(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	p := g.players["alice"]
	ttlBefore := p.TTL

	g.OnQuit("Alice!a@h")
	if p.Online {
		t.Error("player should be offline after OnQuit")
	}
	if p.TTL <= ttlBefore {
		t.Error("OnQuit should apply penalty")
	}
}

func TestOnKick(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	p := g.players["alice"]
	ttlBefore := p.TTL

	g.OnKick("Alice")
	if p.Online {
		t.Error("player should be offline after OnKick")
	}
	if p.TTL <= ttlBefore {
		t.Error("OnKick should apply penalty")
	}
}

func TestOnNick(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	p := g.players["alice"]
	ttlBefore := p.TTL

	g.OnNick("Alice!a@h", "Alicia")
	if _, ok := g.players["alice"]; ok {
		t.Error("old key should be removed after nick change")
	}
	if _, ok := g.players["alicia"]; !ok {
		t.Error("new key should exist after nick change")
	}
	if p.Nick != "Alicia" {
		t.Errorf("p.Nick = %q, want Alicia", p.Nick)
	}
	if p.TTL <= ttlBefore {
		t.Error("OnNick should apply penalty")
	}
}

// --- OnPrivmsg ---

func TestOnPrivmsg(t *testing.T) {
	g := newTestGame()
	g.CmdRegister("Alice!a@h", "Warrior", "pass")
	g.CmdLogin("Alice!a@h", "pass")
	p := g.players["alice"]
	ttlBefore := p.TTL

	text := "hello world" // 11 chars
	g.OnPrivmsg("Alice!a@h", text)
	if p.TTL <= ttlBefore {
		t.Error("OnPrivmsg should apply talk penalty")
	}
}

// --- quest command ---

func TestCmdQuestNoQuest(t *testing.T) {
	g := newTestGame()
	msg := g.CmdQuest()
	if !strings.Contains(msg, "No quest") {
		t.Errorf("expected no-quest message, got %q", msg)
	}
}

// --- rateCheck ---

func TestRateCheck(t *testing.T) {
	// multiplier 0 or negative: never fires.
	for i := 0; i < 100; i++ {
		if rateCheck(86400, 0) {
			t.Error("rateCheck with multiplier 0 should never fire")
		}
		if rateCheck(86400, -1) {
			t.Error("rateCheck with negative multiplier should never fire")
		}
	}

	// Large multiplier collapses denominator to 1: always fires.
	for i := 0; i < 10; i++ {
		if !rateCheck(1, 9999) {
			t.Error("rateCheck with huge multiplier should always fire")
		}
	}

	// Default rate (1.0) over many trials should produce roughly 1/N hits.
	// With denominator=2 and 10000 trials we expect ~5000 hits; allow wide margin.
	hits := 0
	const trials = 10000
	for i := 0; i < trials; i++ {
		if rateCheck(2, 1.0) {
			hits++
		}
	}
	if hits < 3000 || hits > 7000 {
		t.Errorf("rateCheck(2, 1.0) over %d trials: %d hits, want ~5000", trials, hits)
	}

	// 2× multiplier should fire roughly twice as often as 1×.
	hits1x, hits2x := 0, 0
	for i := 0; i < trials; i++ {
		if rateCheck(100, 1.0) {
			hits1x++
		}
		if rateCheck(100, 2.0) {
			hits2x++
		}
	}
	if hits2x < hits1x {
		t.Errorf("2× multiplier (%d hits) should fire more than 1× (%d hits)", hits2x, hits1x)
	}
}

// --- Rates wiring ---

func TestRatesAppliedToGame(t *testing.T) {
	g := newTestGame()
	if g.Rates.PlayerEvents != 1.0 || g.Rates.AlignmentEvents != 1.0 || g.Rates.ServerEvents != 1.0 {
		t.Errorf("newGame Rates = %+v, want all 1.0", g.Rates)
	}
}

// --- weightedItemLevel ---

func TestWeightedItemLevel(t *testing.T) {
	// When min == max the function should return min.
	if got := weightedItemLevel(5, 5); got != 5 {
		t.Errorf("weightedItemLevel(5,5) = %d, want 5", got)
	}
	// Result must always be within [min, max].
	for i := 0; i < 1000; i++ {
		v := weightedItemLevel(10, 20)
		if v < 10 || v > 20 {
			t.Errorf("weightedItemLevel(10,20) = %d, out of range", v)
		}
	}
}

// --- rollItemDrop ---

func TestRollItemDropSlotRange(t *testing.T) {
	p := &Player{Level: 1}
	for i := 0; i < 500; i++ {
		slot, level, _, _ := rollItemDrop(p)
		if slot < 0 || slot > 9 {
			t.Errorf("slot %d out of range", slot)
		}
		if level < 1 {
			t.Errorf("item level %d < 1", level)
		}
	}
}

func TestRollItemDropRarityUnlockLevels(t *testing.T) {
	// Below uncommonMinLevel (25) no named item should ever drop.
	p := &Player{Level: 24}
	for i := 0; i < 10000; i++ {
		_, _, name, rarity := rollItemDrop(p)
		if rarity != rarityNormal || name != "" {
			t.Fatalf("level 24 got rarity %q name %q, want normal", rarity, name)
		}
	}
}

// --- buildTopic ---

func TestBuildTopicEmpty(t *testing.T) {
	g := newTestGame()
	g.mu.Lock()
	topic := g.buildTopic()
	g.mu.Unlock()
	if !strings.Contains(topic, "IdleRPG") {
		t.Errorf("topic should contain IdleRPG, got %q", topic)
	}
}

func TestBuildTopicWithPlayers(t *testing.T) {
	g := newTestGame()
	p := addPlayer(g, "Alice", "Warrior")
	p.Level = 5
	p.Online = true

	g.mu.Lock()
	topic := g.buildTopic()
	g.mu.Unlock()

	if !strings.Contains(topic, "Alice") {
		t.Errorf("topic should contain top player, got %q", topic)
	}
	if !strings.Contains(topic, "1/1") {
		t.Errorf("topic should show 1/1 online, got %q", topic)
	}
}
