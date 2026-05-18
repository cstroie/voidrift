# GoIdle — IdleRPG IRC Bot

[![Go](https://img.shields.io/badge/go-1.21+-00ADD8?logo=go)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/license-MIT-green)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/cstroie/idlerpg)](https://goreportcard.com/report/github.com/cstroie/idlerpg)

A standalone IRC bot implementing the classic [IdleRPG](https://idlerpg.net/) game, written in Go.

Players register a character, pick a class and alignment, and gain levels simply by idling in the channel. Talking, changing nick, parting, quitting, or getting kicked adds penalty time. Characters battle each other on level-up, find items, dual-class, join guilds, go on quests, and roam a 500×500 map — all without lifting a finger.

## Usage

```bash
go build
./idlerpg -server irc.libera.chat:6667 -nick GoIdle -channel "#idlerpg"
```

All flags:

| Flag | Default | Description |
|------|---------|-------------|
| `-server` | `irc.libera.chat:6667` | IRC server `host:port` |
| `-nick` | `GoIdle` | Bot nick |
| `-password` | _(none)_ | Server password |
| `-ssl` | `false` | Use SSL |
| `-channel` | `#idlerpg` | Game channel |
| `-data` | `idlerpg.json` | Player data file (JSON, created automatically) |
| `-guilds` | `guilds.json` | Guild data file (JSON, created automatically) |
| `-dev` | `false` | Dev mode: auto-login channel members on startup and speed up TTL by 5× |

## Player Commands

### Character

| Command | Description |
|---------|-------------|
| `!register <nick> <class> <pass>` | Create a character. Class may be multiple words; password is always last. |
| `!login <pass>` | Log in manually (auto-login happens on channel join). |
| `!logout` | Go offline. |
| `!dualclass <class>` | Choose a permanent second class at level 12+. |
| `!align <good\|neutral\|evil>` | Set alignment. Changing costs penalty time. |
| `!status [nick]` | Level, TTL, alignment, class focus slot, quest status. |
| `!whoami` | Shortcut for your own status. |
| `!top` | Top 5 players by level. |
| `!items [nick]` | Full item loadout with unique names. |
| `!pos [nick]` | Your grid coordinates and any co-located players. |

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
| Uncommon | 25 | 5% | 1.5× – 2× level |
| Rare | 35 | 2% | 2× – 3× level |
| Legendary | 50 | 0.5% | 3× – 5× level (min 50–100) |

Uncommon, Rare, and Legendary items have procedurally generated names (*Ethereal Aegis*, *Primordial Crown*, etc.) and are announced in channel with special markers.

### Class & Dual-Classing

Your class name is free-form. A **focus slot** is derived from it deterministically (FNV-1a hash mod 10) — that slot counts **double** in all battle roll calculations.

At level 12+, use `!dualclass <class>` to permanently add a second class. Both focus slots then count double (or triple if they happen to be the same slot).

Use `!status` to see your current focus slot(s).

### Alignment

Set with `!align <good|neutral|evil>`. Changing alignment costs penalty time.

| Alignment | Battle power | Crit chance | Special event (~1/8–12 days) |
|-----------|-------------|-------------|------------------------------|
| Good | +10% | 1/50 | Paired with another good player → both gain 5–12% TTL |
| Evil | −10% | 1/20 | Steal an item from a good player, or get forsaken (+1–5% TTL) |
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

1. **TTL calamity** — 5–12% TTL increase
2. **TTL godsend** — 5–12% TTL decrease
3. **Item calamity** — one equipped item degraded 5–12%
4. **Item godsend** — one item improved 5–12%
5. **Found item** — stumble upon a roadside item; equipped if better

### Hand of God

~Once per 20 server-days, a random online player is touched by divine power: 80% chance of 5–75% TTL reduction, 20% chance of increase.

### Quests

~Once per day, when 4+ players at level 15+ are online, a quest begins. Four questers are chosen at random.

**Time quest**: complete within 1–3 hours (stay online).  
**Grid quest**: all questers must navigate to a specific map coordinate.

- **Success**: each quester gets −25% TTL
- **Failure**: every online player gets a p15 penalty

### Grid / Map

Players roam a **500×500 toroidal grid**, moving one random step per second. Position is randomised on each login.

When two players share a tile, there is a `1/len(online)` chance of a surprise battle. Use `!pos [nick]` to check your coordinates.

### Persistence

- Player data saved to JSON after every state change. Players start offline after a restart and re-login automatically on channel join.
- Guild data saved to a separate JSON file.
- Item names, alignment, dual-class, and all item slots are persisted.

### Channel Topic

The bot maintains the channel topic with live game state:

```
⚔ IdleRPG | 3/12 idling | Top: Costin lvl 42 Warrior | Quest: slay the dragon [1h left] | ✦ Zara found Primordial Aegis — LEGENDARY!
```

The topic updates on player join/part, every level-up, and after significant events (quests, battles, legendary drops, Hand of God).

## License

MIT
