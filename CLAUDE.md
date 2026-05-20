# Void Drift — Project Guide for Claude

## What This Is

A standalone IRC bot implementing the Void Drift idle game, written in Go.
Players gain levels by idling in the channel. Activity (talking, nick changes,
parting, quitting, getting kicked) adds penalty time. See README.md for player
commands and game mechanics.

## Build & Run

```bash
go build
./voidrift -server irc.libera.chat:6667 -nick VoidKeeper -channel "#voidrift"
```

All flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-server` | `irc.libera.chat:6667` | IRC server `host:port` |
| `-nick` | `VoidKeeper` | Bot nick |
| `-password` | _(none)_ | Server password |
| `-ssl` | `false` | Use SSL |
| `-channel` | `#voidrift` | Game channel |
| `-data` | `voidrift.json` | Player data file |
| `-guilds` | `guilds.json` | Guild data file |
| `-dev` | `false` | Dev mode: TTL ÷14, event rates ×10, weak creeps, easy quests, auto-login channel members |
| `-nickserv` | _(none)_ | NickServ password; sends `IDENTIFY` on connect |
| `-rate-player` | `1.0` | Per-player event multiplier (random events, bot battles) |
| `-rate-align` | `1.0` | Alignment event multiplier (good/evil daily events) |
| `-rate-server` | `1.0` | Server event multiplier (team battles, guild battles, quests, Hand of God) |

Build and test with `go build ./...` and `go test ./...`.

## Code Structure

| File | Purpose |
|------|---------|
| `main.go` | IRC wiring (fluffle/goirc), event dispatch, command routing, reconnect loop |
| `game.go` | Core game logic: players, tick loop, events, battles, quests, grid, creeps, persistence |
| `guild.go` | Guild system: data types, commands, guild battles, persistence |
| `items.go` | Unique/legendary item system: rarity tiers, name generation, `!items` command |
| `achievements.go` | Achievement/title system: definitions, unlock checks, `!achievements` command |
| `suggest.go` | Themed name/class wordlists; `SuggestForNick` used in JOIN handler |
| `go.mod` / `go.sum` | Module: `github.com/cstroie/voidrift`, requires `fluffle/goirc` |

## Player Commands

| Command | Description |
|---------|-------------|
| `!register <name> <pass> <class> [m/f/n]` | Create a character; all fields are single tokens (no spaces); gender optional (m/f/n) |
| `!login <pass>` | Log in manually |
| `!logout` | Go offline |
| `!passwd <old> <new>` | Change password |
| `!gender <m/f/n>` | Change pronoun setting (costs p50) |
| `!dualclass <class>` | Choose a second class at level 12+ (permanent) |
| `!align <good\|neutral\|evil>` | Set alignment (costs p75 to change) |
| `!status [nick]` | Level, TTL, alignment, class focus, title, quest status |
| `!whoami` | Shortcut for your own status |
| `!stats [nick]` | Idled time, timestamps, total and per-source penalty breakdown |
| `!achievements [nick]` | Earned titles and progress toward next unlock in each category |
| `!top` | Top 5 players by level |
| `!items [nick]` | Full item loadout with unique names |
| `!pos [nick]` | Grid coordinates and co-located players |
| `!map` | 11×7 ASCII minimap centred on caller; shows players, creeps, quest target |
| `!online` | List all currently online players |
| `!quest` | Show active quest details and time remaining |
| `!gcreate <name>` | Found a guild (costs p100) |
| `!ginvite <nick>` | Invite a player (leader only) |
| `!gaccept` / `!gdecline` | Accept or decline an invite |
| `!gleave` | Leave guild (auto-transfers leadership) |
| `!gkick <nick>` | Kick a member (leader only) |
| `!ginfo [name]` | Guild details: members, levels, online count |
| `!gtop` | Top 5 guilds by combined level |

## Key Design Points

- `Game.players` is a `map[string]*Player` keyed by **lowercase nick**.
- `Game.guilds` is a `map[string]*Guild` keyed by **lowercase guild name**.
- All map/player/guild mutations are protected by `Game.mu` (`sync.Mutex`).
- The tick goroutine runs every second; `start()` closes the previous stop channel
  before spawning a new one (prevents goroutine leaks on reconnect).
- `say()`, `updateTopic()`, and other outbound calls must happen **outside** the mutex.
  Collect messages into a `[]string` inside the lock, then send after releasing.
- Players are identified by their full `nick!user@host` address (`Player.Addr`)
  to prevent impersonation via nick squatting. Auto-login fires on channel JOIN.
- Passwords are SHA-256 hashed with a per-player 16-byte random salt (`PassSalt`).
- Player data is persisted to JSON after every state change; guild data to a separate
  file. All players start offline on load; position (X, Y) is randomised on each login.

## Player Struct Fields

```
Nick                  — IRC nick, used as map key and for auto-login (lowercase)
Name                  — character display name chosen at registration (shown in all game messages)
Class, Class2         — primary and optional second class (dual-classing at lvl 12)
Gender                — "m"/"f"/"n"; controls pronoun substitution in event messages
PassSalt, PassHash    — salted SHA-256 password
Alignment             — int8: -1 evil, 0 neutral, 1 good
Level, TTL            — level and seconds to next level
Items [10]int         — item level per slot (implant/beacon/module/weapon/visor/suit/gauntlets/plating/deflector/boots)
ItemNames [10]string  — unique name for each slot; empty = normal item
Online, Addr          — session state
X, Y                  — grid position on 500×500 toroidal map (reset on login)

BattlesWon            — cumulative 1v1 battle wins
QuestsCompleted       — cumulative successful quests
CreepsSlain           — cumulative hostile creep defeats
Achievements []string — IDs of earned achievements in unlock order

CreatedAt, LastLogin  — timestamps
TotalIdled            — cumulative seconds spent idling (TTL decrementing while online)
PenMesg/Nick/Part/Kick/Quit/Quest/Other — scaled penalty seconds by source
```

## Game Systems

### Tick Loop (every second)
- Decrement TTL for every online player; increment TotalIdled; check achievements; queue level-ups
- Per-player: random events (~6/day), bot battles (~2/day), alignment events
- Grid: move every player and creep ±1 step; detect co-tile encounters (battle/trade/pass-by)
  and creep encounters; track grid quest progress
- Global: Hand of God (~1/20 days), void storm (~1/40 days), team battles (6+ online, ~8/day),
  guild battles (~4/day), quest start (~4/day), quest timeout resolution
- After the lock: send messages, run battles, call `updateTopic`

### Random Events (6 types, ~equal probability)
1. TTL calamity (5–12% increase, flavour text)
2. TTL godsend (5–12% decrease, flavour text)
3. Item calamity (one slot degraded 5–12%)
4. Item godsend (one slot improved 5–12%)
5. Found item (roadside find with generated name, equipped if better)
6. Warp (teleport ±level×10 tiles in each axis)

### Battles
- **1v1 (level-up)**: `rand(0, effectiveItemSum)` each side; higher wins.
  Winner: −`max(loser_level/4, 7)`% TTL. Loser: +same. Crits double the swing.
- **Bot battle**: bot sum = 1 + highest effectiveItemSum. Win: −12–25% TTL. Loss: +5–15%.
- **Team battle (3v3)**: sum of effectiveItemSum per team; winner −20%, loser +15% TTL.
- **Guild battle**: sum of effectiveItemSum of online members; winner −12–25%, loser +5–15%.
- **Encounter**: co-tile players on the grid; 50% battle, 30% trade, 20% pass-by.
- **Post-battle steal**: winner has 3% chance to take a slot from the loser.

### effectiveItemSum
```
effectiveItemSum(p) = itemSum() + Items[focus(Class)] [+ Items[focus(Class2)]]
```
Focus slot derived via FNV-1a hash of the lowercase class name mod 10.
Dual-classed players add both focus bonuses; same slot stacks (counts triple).

### Alignment
| Alignment | Battle sum | Crit chance | Daily event |
|-----------|-----------|-------------|-------------|
| Good | +10% | 1/50 | Pair with another good player → both −5–12% TTL |
| Evil | −10% | 1/20 | Steal from a good player, or get forsaken (+1–5% TTL) |
| Neutral | normal | none | none |

### Item Slots
`implant`, `beacon`, `module`, `weapon`, `visor`, `suit`, `gauntlets`, `plating`, `deflector`, `boots`

### Item Rarities (level-up drops only)
| Rarity | Unlock | Chance | Level range | Topic marker |
|--------|--------|--------|-------------|--------------|
| Normal | any | always | 1–1.5× level | — |
| Reclaimed | 25 | 5% | 1.5×–2× level | — |
| Architect | 35 | 2% | 2×–3× level | `★` |
| Void-eternal | 50 | 0.5% | 3×–5× level (min 50–100) | `✦` |

Unique items have procedurally generated names (prefix + slot noun) stored in `ItemNames`.
Creep drops are always normal rarity, level 1–creep.Level (40% drop chance on hostile kill).

### Quest System
- Triggers ~4/day when 4+ players at level 15+ are online (in DevMode: 1 player, any level).
- 50% chance of **grid quest** (questers must reach target coordinates) vs **time quest**.
- Grid quest: resolves immediately when all questers step onto (QX, QY).
- Time quest: resolves when the timer (1–3 hours) expires.
- Success: all questers get −20–30% TTL and `QuestsCompleted++`. Failure: all online players get p15 penalty.

### Creeps
- 10 creeps roam the grid at all times; defeated hostile creeps respawn immediately.
- **Hostile** (8 types: Null-wraith, Drift Pirate, Void Predator, etc.): battle any
  co-tile player. Win: −10–20% TTL + 40% chance of item drop. Loss: +7–14% TTL.
- **Peaceful** (4 types: Wandering Archivist, Echo Drifter, etc.): 40% chance of −5–10%
  TTL boon, 60% flavour pass-by.
- In DevMode creep levels are capped at 10.

### Achievement System
- 20 achievements across 5 categories: level milestones, battle wins, creep kills,
  quest completions, idle time and item rarity.
- Checks run in `tickPlayers`, `battle`, `tickCreeps`, `resolveQuest`, and `doLevelUp`.
- Unlocks are announced to the channel immediately.
- Highest-tier earned title is shown in `!status` as `[Title]`.
- `!achievements [nick]` shows earned titles and progress toward next milestone.

### Grid / Map
- 500×500 toroidal grid. Players and creeps move ±1 step per second (random walk).
- Position randomised on each login; not persisted.
- Co-tile players: `1/len(online)` chance of encounter per second.
- `!pos [nick]` shows coordinates, co-located players, and flags quest destinations.
- `!map` renders an 11×7 ASCII minimap: `@` self, letter = player, `!` hostile creep,
  `~` peaceful creep, `*` quest target, `·` empty.

### Channel Topic
`Game.setTopic` (wired in `main.go`) is called by `updateTopic()` after:
- Bot connects, player joins/parts/quits, every level-up, any tick with notable events.
- Format: `🌀 Void Drift | N/M idling | Top: Nick lvl N Class | Quest/event info | last event`
- `noteEvent(msg)` records a short string in `Game.lastEvent` and calls `updateTopic()`.
- Must NOT be called while holding `mu`.

## Adding New Events

1. Add message template strings as package-level `var` slices near the top of `game.go`.
2. Implement the event as a method that takes `*Player` (or a slice) and returns `string`
   or `[]string`. It must be called **with `mu` held**; messages are returned, not sent.
3. Wire it into `tick()` under the appropriate probability check.
4. Keep rates consistent: individual per-player events use `mathrand.Intn(86400)`,
   server-wide events use larger denominators.
5. For notable events, set `g.lastEvent` inside the lock; `updateTopic()` is called
   outside the lock at the end of the tick.

## IRC Event Handlers (main.go)

| IRC event | Game call | Penalty |
|-----------|-----------|---------|
| JOIN | `OnJoin` | none (auto-login + position randomise + suggest if unregistered) |
| PART | `OnPart` | p200 |
| QUIT | `OnQuit` | p20 |
| NICK | `OnNick` | p30 (also updates guild membership) |
| KICK | `OnKick` | p50 (bot auto-rejoins if kicked) |
| PRIVMSG | `OnPrivmsg` | 1s/char (channel only, non-command) |

## Penalty Tracking

`applyPenalty(p, base, kind)` computes `base × 1.14^level` seconds, adds to `p.TTL`,
and increments the matching counter (`PenMesg`, `PenNick`, `PenPart`, `PenKick`,
`PenQuit`, `PenQuest`, `PenOther`). `penTotal()` sums all counters.

## Random Number Usage

`math/rand` is aliased as `mathrand` to avoid collision with `crypto/rand`
(used only for salt generation). Use `mathrand` for all game randomness.

## Research

See `RESEARCH.md` for detailed notes on the original idle RPG mechanics and
what other implementations have done. See `TODO.md` for planned and completed features.
