# IdleRPG Research Notes

Sources: original Perl bot (idlerpg.net), Gelbpunkt/IdleRPG (Discord, Python),
falsovsky/idlerpg (IRC), IdleRPG wiki, community documentation.

---

## Event Types

### Individual Random Events (~1/day per player)
- **TTL calamity** — 5–12% TTL increase
- **TTL godsend** — 5–12% TTL decrease
- **Item calamity** — one random item degraded by 5–12%
- **Item godsend** — one random item improved by 5–12%

### Hand of God (~1/20 days, server-wide)
- 80% chance: 5–75% TTL reduction
- 20% chance: 5–75% TTL increase

### Team Battles (~4/day when 6+ online)
- Two random teams of 3; item sums compared
- Winning team: 20% TTL reduction (scaled to weakest member)
- Losing team: same amount added

### Quests (~1/day, requires 4+ eligible players)
- **Requirements**: 4 players online at level 40+ (original), lowered in many forks
- **Duration**: 12–24 hours time-based, or grid-based (players must reach coordinates)
- **Success**: all 4 questers get 25% TTL reduction
- **Failure**: ALL online players penalised p15

### Alignment Events (not implemented)
- Good players: 1/12 daily chance of light-of-god event with another good player
- Evil players: 1/8 daily chance to steal an item from a good player or get forsaken

---

## Battle Mechanics

### 1v1 (on level-up)
- Roll: `rand(0, item_sum)` each side; higher wins
- Winner: TTL reduced by `max(loser_level/4, 7)%`
- Loser: TTL increased by same amount

### Critical Hits (alignment-based, not implemented)
- Good alignment: 1/50 crit chance
- Evil alignment: 1/20 crit chance

### Post-Battle Item Steal
- Winner has ~2–3% chance to steal one item slot from loser

### Bot Battle (vs the bot itself, not implemented)
- Bot item sum = 1 + highest player sum
- Win: fixed 20% TTL reduction; Loss: fixed 10% TTL increase

---

## Item System

### Slots (10 total)
ring, amulet, charm, weapon, helm, tunic, gloves, leggings, shield, boots

### Finding Items
- One item per level-up; level range 1 to `1.5 × player_level`
- Original uses weighted distribution (higher values exponentially rarer)
- Equipped if it beats the current slot value

### Special Items (not implemented)
- Unique items available after level 25; can exceed 1.5× cap
- Rarity tiers: Common, Uncommon, Rare, Legendary
- Class-specific weapon bonuses (Discord bot feature)

---

## Penalty System

| Event          | Base penalty |
|----------------|-------------|
| Talk in channel | 1s per character |
| Nick change    | 30s |
| Quit IRC       | 20s |
| Part channel   | 200s |
| Kicked         | 50s |

Formula: `base × 1.14^level` seconds added to TTL.

---

## Levelling

- Levels 1–60: `600 × 1.16^N` seconds
- Levels 60+: base_60 + `86400 × (N - 60)` seconds (one day per level added)

---

## Notable Reimplementations

| Project | Language | Platform |
|---------|----------|----------|
| idlerpg.net original | Perl | IRC |
| falsovsky/idlerpg | C | IRC |
| Gelbpunkt/IdleRPG | Python | Discord |
| FabricLabs/idlerpg | JS | Generic |
| cstroie/idlerpg (this) | Go | IRC |

---

## Features in Other Implementations Not Yet Here

- Alignment system (Good / Neutral / Evil) with different event rates and bonuses
- Grid/map system (500×500, players move randomly, location-based encounters)
- Guild system (group membership, guild battles, guild quests)
- Unique/legendary items with rarity tiers
- Class-specific weapon bonuses
- Bot vs. player battles (bot scales to always be competitive)
- Critical hit system tied to alignment
- Evil-player item theft (daily steal attempt against good players)
- Dungeon/adventure system (Discord bot, grid-based)
