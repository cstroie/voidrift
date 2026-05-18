# Void Drift — Project Guide for Claude

## What This Is

A standalone IRC bot implementing the Void Drift idle game, written in Go.
Players gain levels by idling in the channel. Activity (talking, nick changes,
parting, quitting, getting kicked) adds penalty time. See README.md for player
commands and game mechanics.

## Build & Run

```bash
go build
./idlerpg -server irc.libera.chat:6667 -nick VoidKeeper -channel "#voidrift"
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
| `-dev` | `false` | Dev mode: auto-login channel members on startup (via WHO) and speed up TTL by 5× |
| `-nickserv` | _(none)_ | NickServ password; sends `IDENTIFY` on connect |
| `-rate-player` | `1.0` | Per-player event multiplier (random events, bot battles) |
| `-rate-align` | `1.0` | Alignment event multiplier (good/evil daily events) |
| `-rate-server` | `1.0` | Server event multiplier (team battles, guild battles, quests, Hand of God) |

Build and test with `go build ./...` and `go test ./...`.

## Code Structure

| File | Purpose |
|------|---------|
| `main.go` | IRC wiring (fluffle/goirc), event dispatch, command routing, reconnect loop |
| `game.go` | Core game logic: players, tick loop, events, battles, quests, grid, persistence |
| `guild.go` | Guild system: data types, commands, guild battles, persistence |
| `items.go` | Unique/legendary item system: rarity tiers, name generation, `!items` command |
| `go.mod` / `go.sum` | Module: `github.com/cstroie/voidrift`, requires `fluffle/goirc` |

## Player Commands

| Command | Description |
|---------|-------------|
| `!register <class> <pass>` | Create a character (nick taken from IRC nick) |
| `!login <pass>` | Log in manually |
| `!logout` | Go offline |
| `!dualclass <class>` | Choose a second class at level 12+ (permanent) |
| `!align <good\|neutral\|evil>` | Set alignment (costs p75 to change) |
| `!status [nick]` | Level, TTL, alignment, class focus, quest status |
| `!whoami` | Shortcut for your own status |
| `!top` | Top 5 players by level |
| `!items [nick]` | Full item loadout with unique names |
| `!pos [nick]` | Grid coordinates and co-located players |
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
Nick, Class, Class2   — primary and optional second class (dual-classing at lvl 12)
PassSalt, PassHash    — salted SHA-256 password
Alignment             — int8: -1 evil, 0 neutral, 1 good
Level, TTL            — level and seconds to next level
Items [10]int         — item level per slot (ring/amulet/charm/weapon/helm/tunic/gloves/leggings/shield/boots)
ItemNames [10]string  — unique name for each slot; empty = normal item
Online, Addr          — session state
X, Y                  — grid position on 500×500 toroidal map (reset on login)
```

## Game Systems

### Tick Loop (every second)
- Decrement TTL for every online player; queue level-ups
- Per-player: random events (~1/day), alignment events, bot battles (~1/day)
- Global: Hand of God (~1/20 days), team battles (6+ online, ~4/day),
  guild battles (~1/day), quest start (~1/day), quest resolution
- Grid: move every player ±1 step; detect co-tile encounters; track grid quest progress
- After the lock: send messages, run encounter/level-up battles, call `updateTopic`

### Random Events (5 types, equal probability)
1. TTL calamity (5–12% increase, flavour text)
2. TTL godsend (5–12% decrease, flavour text)
3. Item calamity (one slot degraded 5–12%)
4. Item godsend (one slot improved 5–12%)
5. Found item (roadside find, equipped if better)

### Battles
- **1v1 (level-up)**: `rand(0, effectiveItemSum)` each side; higher wins.
  Winner: −`max(loser_level/4, 7)`% TTL. Loser: +same. Crits double the swing.
- **Bot battle**: bot sum = 1 + highest effectiveItemSum. Win: −20% TTL. Loss: +10%.
- **Team battle (3v3)**: sum of effectiveItemSum per team; winner −20%, loser +15% TTL.
- **Guild battle**: sum of effectiveItemSum of online members; winner −20%, loser +15%.
- **Encounter**: co-tile players on the grid; uses standard 1v1 battle logic.
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

### Item Rarities (level-up drops only)
| Rarity | Unlock | Chance | Level range | Topic marker |
|--------|--------|--------|-------------|--------------|
| Normal | any | always | 1–1.5× level | — |
| Reclaimed | 25 | 5% | 1.5×–2× level | — |
| Architect | 35 | 2% | 2×–3× level | `★` |
| Void-eternal | 50 | 0.5% | 3×–5× level (min 50–100) | `✦` |

Unique items have procedurally generated names (prefix + slot noun) stored in `ItemNames`.

### Quest System
- Triggers ~1/day when 4+ players at level 15+ are online.
- 50% chance of **grid quest** (questers must reach target coordinates) vs **time quest**.
- Grid quest: resolves immediately when all questers step onto (QX, QY).
- Time quest: resolves when the timer (1–3 hours) expires.
- Success: all questers get −25% TTL. Failure: all online players get p15 penalty.

### Grid / Map
- 500×500 toroidal grid. Players move ±1 step per second (random walk).
- Position randomised on each login; not persisted.
- Co-tile players have a `1/len(online)` chance of a surprise battle each second.
- `!pos [nick]` shows coordinates, co-located players, and flags quest destinations.

### Channel Topic
`Game.setTopic` (wired in `main.go`) is called by `updateTopic()` after:
- Bot connects, player joins/parts/quits, every level-up, any tick with notable events.
- Format: `⚔ Void Drift | N/M idling | Top: Nick lvl N Class | Quest/event info | last event`
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
| JOIN | `OnJoin` | none (auto-login + position randomise) |
| PART | `OnPart` | p200 |
| QUIT | `OnQuit` | p20 |
| NICK | `OnNick` | p30 (also updates guild membership) |
| KICK | `OnKick` | p50 (bot auto-rejoins if kicked) |
| PRIVMSG | `OnPrivmsg` | 1s/char (channel only, non-command) |

## Random Number Usage

`math/rand` is aliased as `mathrand` to avoid collision with `crypto/rand`
(used only for salt generation). Use `mathrand` for all game randomness.

## Research

See `RESEARCH.md` for detailed notes on the original idle RPG mechanics and
what other implementations have done. See `TODO.md` for planned and completed features.
