# TODO

## High Priority

- [x] **Alignment system** — Good / Neutral / Evil per player; affects event rates,
      battle crit chance, and item steal eligibility. Store as `Alignment int8` (-1/0/1).
- [x] **Bot vs. player battles** — periodic challenge against the bot itself;
      bot item sum = 1 + highest player sum; win gives 20% TTL reduction, loss 10% penalty.
- [x] **Unique/legendary items** — rare drops after level 25 that exceed the 1.5× cap;
      announce with special message.
- [x] **`!quest` status command** — show active quest details and time remaining.

## Medium Priority

- [x] **Critical hits** — alignment-based crit chance in 1v1 battles
      (Good: 1/50, Evil: 1/20); crits apply an extra TTL swing.
- [x] **Evil-player item theft** — daily steal attempt by evil-aligned players
      against good-aligned players (independent of battle).
- [x] **Guild system** — players can form guilds; guild battles and guild quests.
- [x] **Grid/map system** — 500×500 coordinate space; players move randomly each second;
      location-based 1v1 encounters when two players share a tile.
- [x] **Class bonuses** — focus slot derived from class name via FNV-1a hash;
      that slot counts double in all battle rolls.

## Low Priority / Nice to Have

- [x] **`!items` command** — show a player's full item loadout by slot.
- [x] **`!online` command** — list currently online players.
- [x] **Weighted item drops** — use `1/(1.4^N)` probability curve so higher-level
      items are exponentially rarer (currently uniform).
- [x] **NickServ/SASL auth** — identify the bot to NickServ on connect.
- [x] **Configurable rates** — expose event frequency multipliers as CLI flags.
- [x] **Unit tests** — test penalty formula, TTL formula, battle roll logic,
      quest resolution without needing a live IRC connection.
- [x] **Creeps** — NPCs roaming the 500×500 grid (10 active at all times). Hostile
      types (Null-wraith, Drift Pirate, Void Predator, Architect Sentinel, etc.) battle
      players on contact; peaceful types (Wandering Archivist, Echo Drifter, etc.) may
      grant a small TTL boon or simply pass by. Defeated hostile creeps respawn elsewhere.

## Recently Added

- [x] **Gender pronouns** — he/she/they per player; used across all event messages and `!status` output.
- [x] **Void storm** — multi-player calamity that hits all online players simultaneously.
- [x] **`!passwd` command** — allow players to change their password in-game.
- [x] **Thematic hit terms** — replaced generic "crit" with flavour-consistent hit language.
- [x] **Sci-fi item slot names** — renamed all item slots to fit the cosmic horror / sci-fi setting.
- [x] **Quest result in topic** — quest resolution summary is shown in the channel topic.
- [x] **Quest drift** — richer quest topic display; warp random event added.
- [x] **Trading encounters** — grid co-tile encounters can result in a trade instead of a battle.
- [x] **Named roadside finds** — roadside item finds now generate a unique item name for the slot.
- [x] **Varied level-up item narratives** — 5 randomly chosen framings for level-up item drop messages.
- [x] **Increased event frequencies** — bot battles, server events, and quests all fire more often.
- [x] **Name suggestion on join** — unregistered players receive a PM with a thematic name/class suggestion from local wordlists.
- [x] **Player stat tracking** — `CreatedAt`, `LastLogin`, `TotalIdled`, and per-source penalty counters (`PenMesg/Nick/Part/Kick/Quit/Quest/Other`) added to Player; ready for `!stats`.

## Next Up

- [x] **Creep drops** — hostile creeps have a 40% chance to drop an item on defeat; item level scales with creep level.
- [x] **`!map` command** — 11×7 ASCII minimap centred on the player; shows other players (first letter), hostile creeps (`!`), peaceful creeps (`~`), quest target (`*`), with a legend line.
- [x] **Achievements / titles** — 20 achievements across 5 categories (level, battle, creeps, quests, idle/items); highest-tier title shown in `!status`; `!achievements [nick]` shows earned titles and progress toward next unlock in each category.
- [ ] **Seasonal events** — time-limited server-wide events with unique mechanics tied to real-world calendar dates.
- [x] **Player profiles / stats** — `!stats [nick]` shows total idled time, account created date, last login, total penalty time, and per-source penalty breakdown (mesg/nick/part/kick/quit/quest/other as % of total).

## Bugs / Polish

- [x] `CmdRegister` now uses the caller's IRC nick; the explicit nick argument was removed.
- [x] Quest failure should not penalise players who joined after the quest started.
- [x] `save()` acquires the lock internally but callers sometimes hold it already —
      audit all call sites to ensure no double-lock.
