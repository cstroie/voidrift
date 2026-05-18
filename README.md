# Void Drift — IRC IdleRPG Bot

[![Go](https://img.shields.io/badge/go-1.21+-00ADD8?logo=go)](https://go.dev/)
[![License: GPL v3](https://img.shields.io/badge/license-GPLv3-blue)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cstroie/voidrift)](https://goreportcard.com/report/github.com/cstroie/voidrift)

A standalone IRC bot implementing the classic [IdleRPG](https://idlerpg.net/) game, written in Go — with a cosmic horror / dying-world sci-fi skin.

The old gods are gone. What remains are Entities: the Pale Architects, the Drift, the Deep Signal, Protocol ZERO. Players register a character, pick a class and alignment, and gain levels simply by idling in the channel. Talking, changing nick, parting, quitting, or getting kicked adds penalty time. Characters battle each other on level-up, find salvaged artefacts, dual-class, join guilds, go on missions, and roam a 500×500 map — all without lifting a finger.

## Quickstart

**Prerequisites**: Go 1.21 or later.

```bash
git clone https://github.com/cstroie/voidrift.git
cd voidrift
make build
./voidrift -server irc.libera.chat:6667 -nick VoidKeeper -channel "#voidrift"
```

The bot connects, joins the channel, and begins the game loop immediately. Player data is saved automatically to `voidrift.json`; guild data to `guilds.json`.

To test locally without a live IRC server, use dev mode (5× faster TTL, auto-logins existing channel members on connect, and events fire ~100× more often):

```bash
make dev
```

## Building & Testing

```bash
make build   # compile; binary stamped with today's date (yymmdd)
make test    # run unit tests
make run     # build and run with default flags
make dev     # build and run in dev mode
make clean   # remove the binary
```

You can override connection defaults without editing the Makefile:

```bash
make run SERVER=irc.example.org:6667 NICK=MyBot CHANNEL='#mygame'
```

## All Flags

| Flag | Default | Description |
|------|---------|-------------|
| `-server` | `irc.libera.chat:6667` | IRC server `host:port` |
| `-nick` | `VoidKeeper` | Bot nick |
| `-password` | _(none)_ | Server password |
| `-ssl` | `false` | Use SSL |
| `-channel` | `#voidrift` | Game channel |
| `-data` | `voidrift.json` | Player data file (JSON, created automatically) |
| `-guilds` | `guilds.json` | Guild data file (JSON, created automatically) |
| `-nickserv` | _(none)_ | NickServ password — sends `IDENTIFY` on connect |
| `-dev` | `false` | Dev mode: auto-login channel members on startup and speed up TTL by 5× |
| `-rate-player` | `1.0` | Per-player event rate multiplier — scales random events and bot-battle challenges (2.0 = twice as often) |
| `-rate-align` | `1.0` | Alignment event rate multiplier — scales good/evil daily events |
| `-rate-server` | `1.0` | Server event rate multiplier — scales team battles, guild battles, quests, and Hand of God |

## Player Commands

### Character

| Command | Description |
|---------|-------------|
| `!register <class> <pass>` | Create a character using your current IRC nick. Class may be multiple words; password is always last. |
| `!login <pass>` | Log in manually (auto-login happens on channel join). |
| `!logout` | Go offline. |
| `!dualclass <class>` | Choose a permanent second class at level 12+. |
| `!align <good\|neutral\|evil>` | Set alignment. Changing costs penalty time. |
| `!status [nick]` | Level, TTL, alignment, class focus slot, quest status. |
| `!whoami` | Shortcut for your own status. |
| `!top` | Top 5 players by level. |
| `!items [nick]` | Full item loadout with unique names. |
| `!pos [nick]` | Your grid coordinates and any co-located players. |
| `!online` | List all currently online players. |
| `!quest` | Show the active quest, questers, and time remaining. |
| `!help` | Print the command reference in-channel. |

### Guilds

| Command | Description |
|---------|-------------|
| `!gcreate <name>` | Found a guild. You become leader (costs penalty time). |
| `!ginvite <nick>` | Invite a registered player (leader only). |
| `!gaccept` | Accept a pending guild invitation. |
| `!gdecline` | Decline a pending guild invitation. |
| `!gleave` | Leave your guild. Leadership transfers automatically; guild disbands if empty. |
| `!gkick <nick>` | Remove a member (leader only). |
| `!ginfo [name]` | Guild details: leader, members, levels, online count. |
| `!gtop` | Top 5 guilds by combined member level. |

> **Tip:** Use PM for all bot commands to avoid talk penalties.

## Game Mechanics

### Levelling

Players level up passively by idling. Time required for level N:

```
levels 1–60:  600 × 1.16^N  seconds
levels 60+:   base_60 + 86400 × (N − 60)  seconds  (one extra day per level)
```

Level 0→1 takes 10 minutes; level 20 ~2.5 hours; level 60 ~18 days.

### Penalties

Any activity adds time to your next level. Formula: `base × 1.14^level` seconds.

| Event | Base penalty |
|-------|-------------|
| Talking in channel | 1s per character |
| Nick change | 30s |
| Quit IRC | 20s |
| Part channel | 200s |
| Kicked | 50s |
| Change alignment | 75s |
| Found a guild | 100s |

### Items

Ten item slots: **ring, amulet, charm, weapon, helm, tunic, gloves, leggings, shield, boots**.

Each level-up grants a random item. The item is equipped if it beats the current slot value.

#### Rarity tiers

| Rarity | Unlock | Chance | Item level range |
|--------|--------|--------|-----------------|
| Normal | any | always | 1 – 1.5× level |
| Reclaimed | 25 | 5% | 1.5× – 2× level |
| Architect | 35 | 2% | 2× – 3× level |
| Void-eternal | 50 | 0.5% | 3× – 5× level (min 50–100) |

Reclaimed, Architect, and Void-eternal items have procedurally generated names (*Void-touched Resonator*, *Drift-forged Cortex*, *Pale Architect Aegis*, etc.) and are announced in channel with special markers.

### Class & Dual-Classing

Your class name is free-form. A **focus slot** is derived from it deterministically (FNV-1a hash mod 10) — that slot counts **double** in all battle roll calculations.

At level 12+, use `!dualclass <class>` to permanently add a second class. Both focus slots then count double (or triple if they happen to be the same slot).

Use `!status` to see your current focus slot(s).

### Alignment

Set with `!align <good|neutral|evil>`. Changing alignment costs penalty time.

| Alignment | Battle power | Crit chance | Special event (~1/8–12 days) |
|-----------|-------------|-------------|------------------------------|
| Good | +10% | 1/50 | Paired with another good player via resistance network → both gain 5–12% phase |
| Evil | −10% | 1/20 | Entity-compact: steal an item from a good player, or get forsaken (+1–5% TTL) |
| Neutral | normal | none | none |

### Battles

On every level-up the player challenges a random online opponent. Each side rolls `rand(0, effectiveItemSum)`. The higher roll wins.

```
effectiveItemSum = itemSum + Items[focus(Class)] [+ Items[focus(Class2)]]
```

- **Winner**: TTL reduced by `max(loser_level/4, 7)%`
- **Loser**: TTL increased by same amount
- **Critical hit** (good: 1/50, evil: 1/20): swing doubled
- **Post-battle steal**: winner has 3% chance to take one item slot from the loser

### Bot Battles

Each online player has a ~1/day chance of challenging the bot.
Bot power = 1 + highest `effectiveItemSum` across all registered players.

- Win: −20% TTL
- Loss: +10% TTL

### Team Battles

When 6+ players are online, a 3v3 team battle fires ~4 times per day. Teams are randomised; total `effectiveItemSum` per team is compared.

- Winning team: −20% TTL (each member, scaled to weakest)
- Losing team: +15% TTL

### Guild Battles

When 2+ guilds each have 2+ online members, a guild battle fires ~once per day.

- Winning guild's online members: −20% TTL
- Losing guild's online members: +15% TTL

### Random Events

Each online player has a ~1/day chance of a random event:

1. **Calamity** — entropic flux, Null-tide, or a passing Entity costs 5–12% TTL
2. **Godsend** — a pre-collapse navigation burst or Architect relay shortcut saves 5–12% TTL
3. **Item calamity** — one item degraded 5–12% (void-fragment, Drift exposure, micro-collapse)
4. **Item godsend** — one item improved 5–12% (Architect threading, ghost-signal schematics)
5. **Found item** — stumble upon salvage on the grid; equipped if better

### Entity Intervention

~Once per 20 server-days, something vast notices a random online player. 80% chance of 5–75% TTL reduction; 20% chance of increase. It does not explain itself.

### Missions

~Once per day, when 4+ players at level 15+ are online, a mission is issued. Four operatives are chosen at random.

**Time mission**: stay online for 1–3 hours to complete the objective.
**Grid mission**: all operatives must navigate to a specific map coordinate.

Example objectives: *breach the Architect relay station before it completes its transmission*, *sever the Signal tether anchoring the Entity to inhabited space*, *recover the black-box recorder from the vessel that crossed the Veil and did not return*.

- **Success**: each operative gets −25% TTL
- **Failure**: every online player gets a p15 penalty

### Grid / Map

Players roam a **500×500 toroidal grid**, moving one random step per second. Position is randomised on each login.

When two players share a tile, there is a `1/len(online)` chance of a surprise battle. Use `!pos [nick]` to check your coordinates.

### Persistence

- Player data saved to JSON after every state change via atomic rename; file mode `0600`.
- Players start offline after a restart and re-login automatically on channel join.
- Guild data saved to a separate JSON file.
- Item names, alignment, dual-class, and all item slots are persisted.

### Channel Topic

The bot maintains the channel topic with live game state:

```
⚔ Void Drift | 3/12 idling | Top: Costin lvl 42 Warrior | Grid mission: (312,88) — breach the Architect relay station [47m left] | ✦ Zara found Pale Architect Cortex — VOID-ETERNAL!
```

The topic updates on player join/part, every level-up, and after significant events (quests, battles, legendary drops, Hand of God).

## Running as a Service

Init files are in the `init/` directory. Both run the bot inside a chroot
(`/var/lib/voidrift`), so the binary must be built statically — `make build`
sets `CGO_ENABLED=0` automatically.

### systemd (Linux)

The unit uses `RootDirectory=/var/lib/voidrift`. The binary and data files
live directly in that directory; `/etc/resolv.conf` and `/etc/ssl/certs` are
bind-mounted read-only by systemd so DNS and TLS work without putting anything
else in the chroot.

```bash
# Build a static binary
make build

# Create a dedicated user and the chroot root (owned by root)
useradd -r -s /sbin/nologin voidrift
install -dm755 /var/lib/voidrift
chown root:root /var/lib/voidrift

# Install the binary into the chroot root
install -m755 voidrift /var/lib/voidrift/voidrift

# Install and enable the service
install -Dm644 init/voidrift.service /etc/systemd/system/voidrift.service
systemctl daemon-reload
systemctl enable --now voidrift
```

Pass additional flags (e.g. `-nickserv`) by editing `ExecStart` in the unit
file, or drop an override in `/etc/systemd/system/voidrift.service.d/override.conf`.

### OpenRC (Alpine Linux)

The init script runs `chroot --userspec=voidrift:voidrift /var/lib/voidrift`
and bind-mounts `/etc/resolv.conf`, `/etc/nsswitch.conf`, and `/etc/ssl/certs`
read-only into the chroot in `start_pre`, unmounting them on stop.

```bash
# Build a static binary
make build

# Create a dedicated user and the chroot root (owned by root)
adduser -S -D -s /sbin/nologin voidrift
install -dm755 /var/lib/voidrift
chown root:root /var/lib/voidrift

# Install the binary into the chroot root
install -m755 voidrift /var/lib/voidrift/voidrift

# Install and enable the service
install -Dm755 init/voidrift.openrc /etc/init.d/voidrift
rc-update add voidrift default
rc-service voidrift start
```

Extra flags go in `command_args` inside `/etc/init.d/voidrift`.

## Contributing

Bug reports and pull requests are welcome. Please:

1. Fork the repo and create a feature branch.
2. Run `make test` before submitting — all tests must pass.
3. Keep each PR focused; one change per PR.

## Maintainer

Costin Stroie — <costinstroie@eridu.eu.org>

## License

[GNU General Public License v3.0](LICENSE)
