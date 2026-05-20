# Void Drift — Player Manual

## The World

The old gods are gone. What remains are Entities: the Pale Architects, the Drift,
the Deep Signal, Protocol ZERO. Civilisation has collapsed into signal noise and
salvaged debris. You are what persists.

Register a character, choose a class, pick a side, and simply exist in the channel.
The longer you idle without speaking or moving, the stronger you become. Every
word costs time. Every action sets you back. Silence is power.

---

## Getting Started

Send these commands as a **private message to the bot** to avoid triggering talk
penalties. All commands work in PM or in-channel; PM is strongly preferred.

### 1. Register

```
!register <name> <password> <class> [m/f/n]
```

- `name` — your character's display name; **one word, no spaces** (e.g. `Sora-Voidborn`)
- `password` — keep this private; the bot stores it hashed
- `class` — **one word, no spaces** (e.g. `VoidMonk`, `DriftEngineer`, `NullWitch`)
- gender is optional (`m` / `f` / `n`); affects pronouns in event messages

Not sure what to pick? Type `!suggest` for a random name and class combination.

You auto-login when you join the channel. If the bot misses your join, use:

```
!login <password>
```

### 2. Check Your Status

```
!status         — your level, TTL, alignment, focus slot, quest, title
!whoami         — same as above, shortcut
!stats          — account age, total idle time, penalty breakdown
!items          — your full gear loadout
!achievements   — earned titles and progress toward next unlock
```

---

## Levelling

Time to next level for level N:

| Level range | Formula |
|-------------|---------|
| 1 – 60 | 600 × 1.16^N seconds |
| 61+ | base_60 + 86400 × (N − 60) seconds |

**Level 1** takes ~10 minutes. **Level 20** takes ~2.5 hours. **Level 60** takes ~18 days.
Above 60 each level adds another full day. There is no level cap.

You do not need to do anything. Just stay in the channel and do not talk.

---

## Penalties

Any activity adds time to your TTL (time to next level). The formula is:

```
penalty = base × 1.14^level  seconds
```

Higher levels are punished harder for the same action.

| Event | Base penalty |
|-------|-------------|
| Talking in channel | 1 second per character |
| Changing your nick | 30 s |
| Quitting IRC | 20 s |
| Parting the channel | 200 s |
| Getting kicked | 50 s |
| Changing alignment | 75 s |
| Founding a guild | 100 s |

> Use `/msg VoidKeeper !command` instead of typing in-channel.

---

## Classes

Your class name must be a single word (no spaces). Use CamelCase or hyphens to
combine words: `VoidMonk`, `DriftEngineer`, `Pale-Architect`. The bot derives a
**focus slot** from the class name using a deterministic hash. That slot counts
**double** in every battle calculation.

Use `!status` to see which slot your class focuses.

**Item slots** (indices 0–9): `implant`, `beacon`, `module`, `weapon`, `visor`,
`suit`, `gauntlets`, `plating`, `deflector`, `boots`.

---

## Alignment

```
!align <good|neutral|evil>
```

Changing alignment costs p75 penalty. Choose carefully.

| Alignment | Battle power | Crit chance | Special event |
|-----------|-------------|-------------|---------------|
| Good | +10% | 1 in 50 | Paired with another good player via resistance network — both gain 5–12% phase |
| Evil | −10% | 1 in 20 | Entity-compact: steal an item from a good player, or get forsaken (+1–5% TTL) |
| Neutral | normal | none | none |

Evil players hit crits more often but fight at a structural disadvantage. Good
players are weaker individually but gain cooperative windfalls and have a tighter
crit window.

---

## Battles

### Level-Up Battle

Every time you level up you challenge a random online opponent. Each side rolls
`rand(0, effectiveItemSum)`. The higher roll wins.

```
effectiveItemSum = sum of all item slots + Items[focus slot]
```

- **Winner**: TTL reduced by `max(loser_level / 4, 7)%`
- **Loser**: TTL increased by the same amount
- **Critical hit**: the swing is doubled (good: 1/50 chance, evil: 1/20)
- **Post-battle steal**: winner has a 3% chance to take one item slot from the loser

### Bot Battle

~Once per day each online player may face the automated sentinel. Bot power equals
1 plus the highest `effectiveItemSum` among all registered players.

- Win: −20% TTL
- Loss: +10% TTL

### Team Battle

When 6 or more players are online, a random 3v3 team battle fires ~4 times per day.
Teams are drawn at random; total `effectiveItemSum` per team is compared.

- Winning team: −20% TTL each
- Losing team: +15% TTL each

### Guild Battle

When 2 or more guilds each have 2 or more online members, a guild battle fires
~once per day. Combined `effectiveItemSum` of online members is compared.

- Winner's online members: −12–25% TTL
- Loser's online members: +5–15% TTL

### Grid Encounters

Two players sharing the same map tile have a random chance of encountering each
other:

- 50% — surprise battle (same rules as level-up battle)
- 30% — trade (the better item from each side swaps; both may benefit)
- 20% — pass-by (flavour only)

---

## Items

You have ten item slots. Each level-up grants a random item drop. If it beats
your current slot value it is automatically equipped.

### Rarity Tiers

| Rarity | Unlock level | Drop chance | Item level range |
|--------|-------------|------------|-----------------|
| Normal | any | always | 1 – 1.5× your level |
| Reclaimed | 25 | 5% | 1.5× – 2× your level |
| Architect | 35 | 2% | 2× – 3× your level |
| Void-eternal | 50 | 0.5% | 3× – 5× your level (min 50–100) |

Reclaimed items and above have procedurally generated names (e.g., *Drift-forged
Cortex*, *Pale Architect Aegis*) and are announced in channel with markers.
Void-eternal drops are announced with `✦` in the topic.

Hostile creeps also drop items on defeat (40% chance, always Normal rarity,
level 1 to creep level).

### Viewing Your Gear

```
!items [nick]   — full loadout with slot values and unique names
```

---

## Random Events

Each online player has roughly a 1-in-14,400 chance per second (~6 per day) of
a random event:

| Event | Effect |
|-------|--------|
| Calamity | +5–12% TTL (entropic flux, Null-tide, passing Entity) |
| Godsend | −5–12% TTL (Architect relay burst, ghost-signal shortcut) |
| Item calamity | One item slot degraded 5–12% |
| Item godsend | One item slot improved 5–12% |
| Found item | Salvage discovered on the grid; equipped if better |
| Warp | Teleported ±level×10 tiles in each axis |

---

## Entity Intervention (Hand of God)

Approximately once every 20 server-days, something vast notices a random online
player. The outcome is not negotiable and is not explained:

- 80% chance: TTL reduced 5–75%
- 20% chance: TTL increased by a similar amount

---

## Missions

~4 times per day, when 4 or more players at level 15+ are online, a mission is
issued to four randomly chosen operatives.

### Time Mission

Stay online without logging out for the mission duration (1–3 hours). No movement
required — just remain connected.

### Grid Mission

All four operatives must navigate to a specific map coordinate. Characters move
automatically one step per second, so you will drift toward the target naturally —
but hostile creeps and random warps can delay you.

```
!quest          — show the current mission, questers, and time or coordinates
!pos            — check your current position
!map            — ASCII minimap centred on you
```

### Outcomes

- **Success**: each operative gets −25% TTL
- **Failure**: every online player receives a p15 penalty

---

## Creeps

Ten NPCs roam the 500×500 grid at all times. Defeated creeps respawn immediately
at a random location.

### Hostile Creeps

*Null-wraith, Drift Pirate, Void Predator, Architect Sentinel, Rift Crawler,
Signal Phantom*

When a hostile creep shares your tile it attacks. The battle uses normal item-sum
mechanics.

- **Win**: −10–20% TTL + 40% chance of a Normal item drop + 1 kill toward creep achievements
- **Loss**: +7–14% TTL

### Peaceful Creeps

*Wandering Archivist, Echo Drifter, Phantom Surveyor, Pale Cartographer*

When a peaceful creep passes your tile:

- 40% chance of −5–10% TTL boon
- 60% flavour pass-by with no mechanical effect

---

## The Grid

Players and creeps roam a **500×500 toroidal** (wrap-around) map. Position is
randomised on each login. Everyone moves one step per second automatically;
you cannot steer.

```
!pos [nick]     — coordinates, co-located players, quest target flag
!map            — 11×7 ASCII minimap centred on your position
```

Map legend:

| Symbol | Meaning |
|--------|---------|
| `@` | You |
| letter | Another online player |
| `!` | Hostile creep |
| `~` | Peaceful creep |
| `*` | Mission target |
| `·` | Empty tile |

---

## Achievements & Titles

Twenty achievements are tracked across six categories. Earned titles appear in
`!status`. The title shown is always the highest tier you have unlocked.

```
!achievements [nick]    — earned titles and progress toward next unlock
```

| Category | Milestones |
|----------|-----------|
| Level | 5 · 15 · 25 · 35 · 50 · 75 · 100 |
| Battles won | 1 · 10 · 50 |
| Creeps slain | 1 · 10 · 50 |
| Quests completed | 1 · 5 |
| Idle time | 24 h · 7 days |
| Item rarity | Reclaimed · Architect · Void-eternal |

---

## Guilds

```
!gcreate <name>         — found a guild (costs p100 penalty)
!ginvite <nick>         — invite a player (leader only)
!gaccept                — accept a guild invitation
!gdecline               — decline a guild invitation
!gleave                 — leave your guild
!gkick <nick>           — remove a member (leader only)
!ginfo [name]           — guild details: leader, members, levels, online count
!gtop                   — top 5 guilds by combined member level
```

Guild membership grants access to guild battles (see above). Leadership transfers
automatically when the leader leaves. A guild disbands if all members leave.

---

## Channel Topic

The bot keeps the topic up to date with live game state:

```
🌀 Void Drift | 3/12 idling | Top: Nomad lvl 42 Drift Engineer | Grid mission: (312,88) [47m] | ✦ Zara found Pale Architect Cortex — VOID-ETERNAL!
```

The topic updates on every level-up, player join/part, mission event, and
significant drop.

---

## Account Management

```
!passwd <oldpass> <newpass>     — change your password
!gender <m|f|n>                 — change pronoun setting (costs p50)
!rename <name>                  — change your character's name (costs p100)
!reclass <class>                — change your primary class (costs p100)
!logout                         — go offline without leaving the channel
!online                         — list all currently online players
!top                            — top 5 players by level
!all                            — all registered players, one per line, sorted by level; shows class and TTL (* = online)
```

`!rename` and `!reclass` follow the same no-spaces rule as registration: one word,
CamelCase or hyphens. Reclassing shifts your focus slot, which may change your
battle effectiveness.

---

## Strategy Tips

- **Never talk in-channel.** Use `/msg VoidKeeper !command` for everything.
- **Nick changes are cheap early, expensive late.** p30 × 1.14^level grows fast.
- **Good alignment pays off at scale.** The +10% battle bonus and cooperative godsend
  events compound over time; the 1/50 crit is a bonus, not the core advantage.
- **Evil's 1/20 crit beats good's in fast PvP**, but the −10% battle penalty hurts
  against well-geared opponents.
- **Focus slot is your class identity.** Check `!status` to see which slot your class focuses — it counts double in every battle roll, but all upgrades are random so it shapes your character flavour rather than your strategy.
- **Missions are opt-in by existing.** You cannot choose to join or leave; just be
  online, at level 15+, and you may be selected. Logging out avoids selection but
  costs p200.
- **Grid encounters are probabilistic.** You cannot avoid them, but hostile creeps
  become free TTL and item drops once your item sum exceeds their level.
- **Guilds amplify the best players.** A guild battle pools all online members'
  `effectiveItemSum`, so a guild of well-geared players dominates.

---

*Void Drift is a variant of the classic IdleRPG concept, reimagined in a cosmic
horror / dying-world science-fiction setting. Original IdleRPG by Jotun.*
