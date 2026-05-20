// Package main is the entry point for VoidKeeper, the Void Drift IRC bot.
//
// It wires the IRC connection (via fluffle/goirc) to the game engine defined in
// game.go and guild.go. All game logic lives in [Game]; this file is responsible
// only for IRC event dispatch and the reconnect loop.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	irc "github.com/fluffle/goirc/client"
)

// envFlags maps each VOIDRIFT_* environment variable to the corresponding
// flag name. applyEnv must be called before flag.Parse() so that explicit
// command-line flags can still override env values.
var envFlags = map[string]string{
	"VOIDRIFT_SERVER":      "server",
	"VOIDRIFT_NICK":        "nick",
	"VOIDRIFT_PASSWORD":    "password",
	"VOIDRIFT_SSL":         "ssl",
	"VOIDRIFT_CHANNEL":     "channel",
	"VOIDRIFT_DATA":        "data",
	"VOIDRIFT_GUILDS":      "guilds",
	"VOIDRIFT_DEV":         "dev",
	"VOIDRIFT_NICKSERV":    "nickserv",
	"VOIDRIFT_CHANSERV":    "chanserv",
	"VOIDRIFT_RATE_PLAYER": "rate-player",
	"VOIDRIFT_RATE_ALIGN":  "rate-align",
	"VOIDRIFT_RATE_SERVER": "rate-server",
}

// applyEnv sets flag defaults from environment variables. Command-line flags
// take precedence because flag.Parse() runs after this function.
func applyEnv() {
	for env, flagName := range envFlags {
		if val := os.Getenv(env); val != "" {
			if err := flag.Set(flagName, val); err != nil {
				log.Fatalf("invalid value for %s: %v", env, err)
			}
		}
	}
}

// main parses flags, constructs the IRC client and Game, registers all event
// handlers, then runs the reconnect loop forever.
func main() {
	server := flag.String("server", "irc.libera.chat:6667", "IRC server host:port")
	nick := flag.String("nick", "VoidKeeper", "Bot nick")
	password := flag.String("password", "", "Server password")
	ssl := flag.Bool("ssl", false, "Use SSL")
	channel := flag.String("channel", "#voidrift", "Game channel")
	dataFile := flag.String("data", "voidrift.json", "Player data file")
	guildsFile := flag.String("guilds", "guilds.json", "Guild data file")
	dev := flag.Bool("dev", false, "Dev mode: auto-login channel members on startup and speed up TTL by 5×")
	nickservPass := flag.String("nickserv", "", "NickServ password (sends IDENTIFY on connect)")
	chanserv := flag.String("chanserv", "ChanServ", "ChanServ nick to request ops from on channel join (set empty to disable)")
	ratePlayer := flag.Float64("rate-player", 1.0, "Per-player event rate multiplier (random events, bot battles; default 1.0 = ~1/day each)")
	rateAlign := flag.Float64("rate-align", 1.0, "Alignment event rate multiplier (good/evil daily events; default 1.0)")
	rateServer := flag.Float64("rate-server", 1.0, "Server event rate multiplier (team battles, guild battles, quests, Hand of God; default 1.0)")
	applyEnv()
	flag.Parse()

	cfg := irc.NewConfig(*nick, "voidrift", "Void Drift bot")
	cfg.SSL = *ssl
	cfg.Server = *server
	cfg.Pass = *password
	// Append "_" when the preferred nick is already taken rather than failing.
	cfg.NewNick = func(n string) string { return n + "_" }
	conn := irc.Client(cfg)

	// say sends a message to the game channel and logs a plain-text version.
	say := func(msg string) {
		log.Printf(">> %s", stripIRC(msg))
		conn.Privmsg(*channel, msg)
	}

	game := newGame(*dataFile, *guildsFile, say)
	game.DevMode = *dev
	game.Rates = Rates{
		PlayerEvents:    *ratePlayer,
		AlignmentEvents: *rateAlign,
		ServerEvents:    *rateServer,
	}
	game.setTopic = func(topic string) {
		log.Printf("TOPIC: %s", stripIRC(topic))
		conn.Topic(*channel, topic)
	}

	connected := make(chan bool)
	// invitedAt tracks the last time each nick was sent an IRC INVITE so we
	// never invite the same player more than once per hour.
	invitedAt := make(map[string]time.Time)
	var resetWHO func()
	registerHandlers(conn, game, say, connected, *channel, *nick, *nickservPass, *chanserv, *dev, invitedAt, &resetWHO)

	// Reconnect loop: on disconnect wait 10 s then try again indefinitely.
	for {
		log.Println("Connecting to", *server)
		if err := conn.Connect(); err != nil {
			log.Println("Connect error:", err)
			time.Sleep(10 * time.Second)
			continue
		}
		for {
			if ok := <-connected; !ok {
				log.Println("Disconnected, reconnecting in 10s...")
				time.Sleep(10 * time.Second)
				break
			}
		}
	}
}

// registerHandlers attaches all IRC event handlers to conn. It captures game,
// say, and the configuration values it needs via closure. The connected channel
// receives false whenever the connection drops so the reconnect loop can fire.
func registerHandlers(conn *irc.Conn, game *Game, say func(string), connected chan bool,
	channel, botNick, nickservPass, chanserv string, dev bool, invitedAt map[string]time.Time, resetWHO *func()) {

	// welcomedAt deduplicates suggest messages: if the JOIN handler fires more
	// than once for the same nick within 10 seconds (e.g. due to goirc sending
	// an internal WHO after the bot joins, or an IRC server replaying JOINs on
	// reconnect), only the first occurrence sends the welcome DMs.
	welcomedAt := make(map[string]time.Time)

	// maybeInvite sends an IRC INVITE to nick if they are a registered player
	// not currently in the channel, and have not been invited within the last hour.
	maybeInvite := func(c *irc.Conn, nick string) {
		if !game.IsKnownOffline(nick) {
			return
		}
		key := strings.ToLower(nick)
		if time.Since(invitedAt[key]) < time.Hour {
			return
		}
		invitedAt[key] = time.Now()
		c.Raw(fmt.Sprintf("INVITE %s %s", nick, channel))
	}

	conn.HandleFunc("connected", func(c *irc.Conn, line *irc.Line) {
		log.Println("Connected, joining", channel)
		if nickservPass != "" {
			c.Privmsg("NickServ", "IDENTIFY "+nickservPass)
		}
		c.Join(channel)
		game.start()
		// In dev mode, issue WHO immediately so existing channel members are
		// auto-logged in without having to re-join.
		if dev {
			(*resetWHO)()
			c.Who(channel)
		}
	})

	*resetWHO = registerWHOHandlers(conn, game, botNick, dev)

	conn.HandleFunc("JOIN", func(c *irc.Conn, line *irc.Line) {
		if len(line.Args) == 0 || !strings.EqualFold(line.Args[0], channel) {
			return
		}
		joiningNick := extractNick(line.Src)
		if joiningNick == botNick {
			// Request ops from ChanServ whenever the bot joins the channel.
			if chanserv != "" {
				c.Privmsg(chanserv, fmt.Sprintf("OP %s %s", channel, botNick))
			}
			return
		}
		game.OnJoin(line.Src)
		key := strings.ToLower(joiningNick)
		if time.Since(welcomedAt[key]) >= 10*time.Second {
			welcomedAt[key] = time.Now()
			for _, msg := range game.SuggestForNick(joiningNick) {
				c.Privmsg(joiningNick, msg)
			}
		}
	})
	conn.HandleFunc("PART", func(c *irc.Conn, line *irc.Line) { game.OnPart(line.Src) })
	conn.HandleFunc("QUIT", func(c *irc.Conn, line *irc.Line) { game.OnQuit(line.Src) })
	conn.HandleFunc("NICK", func(c *irc.Conn, line *irc.Line) { game.OnNick(line.Src, line.Args[0]) })

	conn.HandleFunc("KICK", func(c *irc.Conn, line *irc.Line) {
		if len(line.Args) < 2 {
			return
		}
		if line.Args[1] == botNick {
			// Rejoin immediately if the bot itself was kicked.
			c.Join(channel)
			return
		}
		game.OnKick(line.Args[1])
	})

	conn.HandleFunc("PRIVMSG", func(c *irc.Conn, line *irc.Line) {
		if len(line.Args) < 2 {
			return
		}
		src, ch, text := line.Src, line.Args[0], strings.TrimSpace(line.Args[1])
		fields := strings.Fields(text)
		if len(fields) == 0 {
			return
		}
		// In IRC, channel names always start with '#', '&', '!', or '+'.
		// Anything else is a DM addressed directly to the bot, regardless of
		// what the bot's current nick is (it may differ from botNick if a
		// collision forced a rename to e.g. "VoidKeeper_").
		replyTo := extractNick(src) // default: reply to sender
		if !isChannel(ch) {
			// DM — reply goes back to the sender as a DM.
		} else if ch == channel {
			// Channel message — penalise for talking and redirect command
			// replies to the sender's PM so the channel stays clean.
			game.OnPrivmsg(src, text)
			if strings.HasPrefix(text, "!") {
				conn.Privmsg(replyTo, "Tip: use PM for bot commands to avoid talk penalties.")
			}
		} else {
			// Some other channel the bot happens to be in — ignore.
			return
		}
		reply := func(msg string) { conn.Privmsg(replyTo, msg) }
		dispatchCommand(src, fields, game, say, reply)
		if !isChannel(ch) {
			maybeInvite(c, extractNick(src))
		}
	})

	conn.HandleFunc("disconnected", func(c *irc.Conn, line *irc.Line) { connected <- false })
}

// registerWHOHandlers wires up the WHO reply (numeric 352) and end-of-WHO
// (numeric 315) handlers used in dev mode to auto-login all players that are
// already in the channel when the bot connects.
//
// whoQueue accumulates nick!user@host strings from 352 replies and is flushed
// into [Game.OnJoin] calls when 315 signals the end of the WHO list.
func registerWHOHandlers(conn *irc.Conn, game *Game, botNick string, dev bool) func() {
	var whoQueue []string
	reset := func() { whoQueue = nil }
	conn.HandleFunc("352", func(c *irc.Conn, line *irc.Line) {
		// Args layout: [botnick, #channel, user, host, server, nick, flags, ...]
		if !dev || len(line.Args) < 6 {
			return
		}
		memberNick := line.Args[5]
		if memberNick == botNick {
			return
		}
		whoQueue = append(whoQueue, fmt.Sprintf("%s!%s@%s", memberNick, line.Args[2], line.Args[3]))
	})
	conn.HandleFunc("315", func(c *irc.Conn, line *irc.Line) {
		if !dev {
			return
		}
		queue := whoQueue
		whoQueue = nil
		log.Printf("Auto-login: %d channel member(s) found", len(queue))
		for _, src := range queue {
			game.OnJoin(src)
		}
	})
	return reset
}

// version is set at build time via -ldflags "-X main.version=YYMMDD".
// It defaults to "dev" when built without the flag (e.g. go run).
var version = "dev"

func init() {
	log.Printf("Void Drift / VoidKeeper starting (version %s)", version)
}

// dispatchCommand routes a parsed IRC command (fields[0] is the command token)
// to the appropriate [Game] method. say broadcasts to the channel; reply sends
// to the originating user (either a channel or a PM, resolved by the caller).
func dispatchCommand(src string, fields []string, g *Game, say, reply func(string)) {
	switch fields[0] {
	case "!register":
		dispatchRegister(src, fields, g, say, reply)
	case "!login":
		dispatchLogin(src, fields, g, reply)
	case "!logout":
		reply(g.CmdLogout(src))
	case "!delete":
		if len(fields) < 2 {
			reply("Usage: !delete <password>  — permanently deletes your account")
			return
		}
		say(g.CmdDelete(src, fields[1]))
	case "!passwd":
		if len(fields) < 3 {
			reply("Usage: !passwd <oldpass> <newpass>")
			return
		}
		reply(g.CmdPasswd(src, fields[1], fields[2]))
	case "!gender":
		if len(fields) < 2 {
			reply("Usage: !gender <m|f|n>  (m=he/him, f=she/her, n=they/them) — costs p50")
			return
		}
		reply(g.CmdGender(src, fields[1]))
	case "!rename":
		if len(fields) < 2 {
			reply("Usage: !rename <name>  — one word, no spaces; costs p100")
			return
		}
		say(g.CmdRename(src, fields[1]))
	case "!reclass":
		if len(fields) < 2 {
			reply("Usage: !reclass <class>  — one word, no spaces; costs p100")
			return
		}
		say(g.CmdReclass(src, fields[1]))
	case "!align":
		dispatchAlign(src, fields, g, reply)
	case "!status":
		reply(g.CmdStatus(src, optArg(fields, 1)))
	case "!whoami":
		reply(g.CmdStatus(src, ""))
	case "!top":
		reply(g.CmdTop())
	case "!all":
		for _, line := range g.CmdAll() {
			reply(line)
			time.Sleep(300 * time.Millisecond)
		}
	case "!online":
		reply(g.CmdOnline())
	case "!quest":
		reply(g.CmdQuest())
	case "!items":
		reply(g.CmdItems(src, optArg(fields, 1)))
	case "!pos":
		reply(g.CmdPos(src, optArg(fields, 1)))
	case "!map":
		for _, line := range g.CmdMap(src) {
			reply(line)
		}
	case "!stats":
		for _, line := range g.CmdStats(src, optArg(fields, 1)) {
			reply(line)
		}
	case "!achievements", "!ach":
		for _, line := range g.CmdAchievements(src, optArg(fields, 1)) {
			reply(line)
		}
	case "!suggest":
		reply(g.Suggest())
	case "!help":
		reply(helpTextAccount)
		reply(helpTextReports)
		reply(helpTextGuilds)
	default:
		dispatchGuildCommand(src, fields, g, say, reply)
	}
}

const helpTextAccount = "Account: " +
	"!register <name> <pass> <class> [m|f|n] (no spaces in any field) | !suggest | " +
	"!login <pass> | !logout | !passwd <old> <new> | !delete <pass> | !gender <m|f|n> | " +
	"!rename <name> | !reclass <class> | !align <good|neutral|evil>"

const helpTextReports = "Reports: " +
	"!status [nick/name/#] | !whoami | !stats [nick/name/#] | " +
	"!achievements [nick/name/#] | !items [nick/name/#] | " +
	"!top | !all | !online | !quest | !pos [nick/name/#] | !map"

const helpTextGuilds = "Guilds: " +
	"!gcreate <name> | !ginvite <nick> | !gaccept | !gdecline | " +
	"!gleave | !gkick <nick> | !ginfo [name] | !gtop"

// dispatchRegister handles !register <name> <pass> <class> [m|f|n].
// All three required fields are single tokens (no spaces allowed).
// The optional fourth token sets gender (m/f/n); defaults to n (they/them).
func dispatchRegister(src string, fields []string, g *Game, say, reply func(string)) {
	const usage = "Usage: !register <name> <pass> <class> [m|f|n]  — no spaces in any field"
	if len(fields) < 4 {
		reply(usage)
		return
	}
	name := fields[1]
	pass := fields[2]
	class := fields[3]
	gender := "n"
	if len(fields) == 5 {
		last := fields[4]
		if last == "m" || last == "f" || last == "n" {
			gender = last
		} else {
			reply(usage)
			return
		}
	} else if len(fields) > 5 {
		reply(usage)
		return
	}
	say(g.CmdRegister(src, name, pass, class, gender))
}

// dispatchLogin handles !login <pass>, replying privately so the outcome
// (including "Wrong password.") is never visible to the channel.
func dispatchLogin(src string, fields []string, g *Game, reply func(string)) {
	if len(fields) < 2 {
		reply("Usage: !login <pass>")
		return
	}
	reply(g.CmdLogin(src, fields[1]))
}

// dispatchAlign handles !align <good|neutral|evil>.
func dispatchAlign(src string, fields []string, g *Game, reply func(string)) {
	if len(fields) < 2 {
		reply("Usage: !align <good|neutral|evil>")
		return
	}
	reply(g.CmdAlign(src, fields[1]))
}

// dispatchGuildCommand handles all !g* guild commands.
func dispatchGuildCommand(src string, fields []string, g *Game, say, reply func(string)) {
	switch fields[0] {
	case "!gcreate":
		if len(fields) < 2 {
			reply("Usage: !gcreate <name>")
			return
		}
		say(g.CmdGCreate(src, strings.Join(fields[1:], " ")))
	case "!ginvite":
		if len(fields) < 2 {
			reply("Usage: !ginvite <nick>")
			return
		}
		reply(g.CmdGInvite(src, fields[1]))
	case "!gaccept":
		say(g.CmdGAccept(src))
	case "!gdecline":
		reply(g.CmdGDecline(src))
	case "!gleave":
		say(g.CmdGLeave(src))
	case "!gkick":
		if len(fields) < 2 {
			reply("Usage: !gkick <nick>")
			return
		}
		say(g.CmdGKick(src, fields[1]))
	case "!ginfo":
		reply(g.CmdGInfo(src, optArg(fields, 1)))
	case "!gtop":
		reply(g.CmdGTop())
	}
}

// isChannel reports whether name is an IRC channel (starts with #, &, !, or +).
func isChannel(name string) bool {
	if len(name) == 0 {
		return false
	}
	switch name[0] {
	case '#', '&', '!', '+':
		return true
	}
	return false
}

// optArg returns fields[i] when the slice is long enough, otherwise "".
// Used for optional command arguments such as the target nick in !status [nick].
func optArg(fields []string, i int) string {
	if i < len(fields) {
		return fields[i]
	}
	return ""
}
