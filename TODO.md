# TODO

## High Priority

- [x] **Alignment system** — Good / Neutral / Evil per player; affects event rates,
      battle crit chance, and item steal eligibility. Store as `Alignment int8` (-1/0/1).
- [ ] **Bot vs. player battles** — periodic challenge against the bot itself;
      bot item sum = 1 + highest player sum; win gives 20% TTL reduction, loss 10% penalty.
- [ ] **Unique/legendary items** — rare drops after level 25 that exceed the 1.5× cap;
      announce with special message.
- [ ] **`!quest` status command** — show active quest details and time remaining.

## Medium Priority

- [x] **Critical hits** — alignment-based crit chance in 1v1 battles
      (Good: 1/50, Evil: 1/20); crits apply an extra TTL swing.
- [x] **Evil-player item theft** — daily steal attempt by evil-aligned players
      against good-aligned players (independent of battle).
- [ ] **Guild system** — players can form guilds; guild battles and guild quests.
- [ ] **Grid/map system** — 500×500 coordinate space; players move randomly each second;
      location-based 1v1 encounters when two players share a tile.
- [ ] **Class bonuses** — weapon-slot bonuses tied to character class
      (e.g. Warriors get +ATK with weapon items).

## Low Priority / Nice to Have

- [ ] **Dual-classing** — choose a second class at level 12 for hybrid bonuses.
- [ ] **`!items` command** — show a player's full item loadout by slot.
- [ ] **`!online` command** — list currently online players.
- [ ] **Weighted item drops** — use `1/(1.4^N)` probability curve so higher-level
      items are exponentially rarer (currently uniform).
- [ ] **NickServ/SASL auth** — identify the bot to NickServ on connect.
- [ ] **Configurable rates** — expose event frequency multipliers as CLI flags.
- [ ] **Unit tests** — test penalty formula, TTL formula, battle roll logic,
      quest resolution without needing a live IRC connection.

## Bugs / Polish

- [ ] `CmdRegister` accepts any nick argument rather than enforcing the caller's IRC nick —
      decide whether this is intended (character names ≠ IRC nicks) or a bug.
- [ ] Quest failure should not penalise players who joined after the quest started.
- [ ] `save()` acquires the lock internally but callers sometimes hold it already —
      audit all call sites to ensure no double-lock.
