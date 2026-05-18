// Package main is the entry point for GoIdle, a standalone IdleRPG IRC bot.
//
// It wires the IRC connection (via fluffle/goirc) to the game engine defined in
// game.go and guild.go. All game logic lives in [Game]; this file is responsible
// only for IRC event dispatch and the reconnect loop.
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	irc "github.com/fluffle/goirc/client"
)

// main parses flags, constructs the IRC client and Game, registers all event
// handlers, then runs the reconnect loop forever.
func main() {
	server := flag.String("server", "irc.libera.chat:6667", "IRC server host:port")
	nick := flag.String("nick", "GoIdle", "Bot nick")
	password := flag.String("password", "", "Server password")
	ssl := flag.Bool("ssl", false, "Use SSL")
	channel := flag.String("channel", "#idlerpg", "Game channel")
	dataFile := flag.String("data", "idlerpg.json", "Player data file")
	guildsFile := flag.String("guilds", "guilds.json", "Guild data file")
	dev := flag.Bool("dev", false, "Dev mode: auto-login channel members on startup and speed up TTL by 5×")
	nickservPass := flag.String("nickserv", "", "NickServ password (sends IDENTIFY on connect)")
	ratePlayer := flag.Float64("rate-player", 1.0, "Per-player event rate multiplier (random events, bot battles; default 1.0 = ~1/day each)")
	rateAlign := flag.Float64("rate-align", 1.0, "Alignment event rate multiplier (good/evil daily events; default 1.0)")
	rateServer := flag.Float64("rate-server", 1.0, "Server event rate multiplier (team battles, guild battles, quests, Hand of God; default 1.0)")
	flag.Parse()

	cfg := irc.NewConfig(*nick, "idlerpg", "IdleRPG bot")
	cfg.SSL = *ssl
	cfg.Server = *server
	cfg.Pass = *password
	// Append "_" when the preferred nick is already taken rather than failing.
	cfg.NewNick = func(n string) string { return n + "_" }
	conn := irc.Client(cfg)

	// say sends a message to the game channel and logs it for debugging.
	say := func(msg string) {
		log.Printf(">> %s", msg)
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
		log.Printf("TOPIC: %s", topic)
		conn.Topic(*channel, topic)
	}

	connected := make(chan bool)
	registerHandlers(conn, game, say, connected, *channel, *nick, *nickservPass, *dev)

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
	channel, botNick, nickservPass string, dev bool) {

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
			c.Who(channel)
		}
	})

	registerWHOHandlers(conn, game, botNick, dev)

	conn.HandleFunc("JOIN", func(c *irc.Conn, line *irc.Line) {
		// Ignore the bot's own JOIN confirmation.
		if extractNick(line.Src) == botNick {
			return
		}
		game.OnJoin(line.Src)
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
		// collision forced a rename to e.g. "GoIdle_").
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
	})

	conn.HandleFunc("disconnected", func(c *irc.Conn, line *irc.Line) { connected <- false })
}

// registerWHOHandlers wires up the WHO reply (numeric 352) and end-of-WHO
// (numeric 315) handlers used in dev mode to auto-login all players that are
// already in the channel when the bot connects.
//
// whoQueue accumulates nick!user@host strings from 352 replies and is flushed
// into [Game.OnJoin] calls when 315 signals the end of the WHO list.
func registerWHOHandlers(conn *irc.Conn, game *Game, botNick string, dev bool) {
	var whoQueue []string
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
}

func init() {
	log.Println("IdleRPG bot starting")
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
	case "!dualclass":
		dispatchDualClass(src, fields, g, reply)
	case "!align":
		dispatchAlign(src, fields, g, reply)
	case "!status":
		reply(g.CmdStatus(src, optArg(fields, 1)))
	case "!whoami":
		reply(g.CmdStatus(src, ""))
	case "!top":
		reply(g.CmdTop())
	case "!online":
		reply(g.CmdOnline())
	case "!quest":
		reply(g.CmdQuest())
	case "!items":
		reply(g.CmdItems(src, optArg(fields, 1)))
	case "!pos":
		reply(g.CmdPos(src, optArg(fields, 1)))
	case "!help":
		reply(helpText)
	default:
		dispatchGuildCommand(src, fields, g, say, reply)
	}
}

// helpText is the single-line command reference sent in response to !help.
const helpText = "IdleRPG commands: " +
	"!register <nick> <class> <pass> | " +
	"!login <pass> | !logout | " +
	"!dualclass <class> (level 12+, permanent) | " +
	"!align <good|neutral|evil> | " +
	"!status [nick] | !whoami | !top | !online | !quest | !items [nick] | !pos [nick] | " +
	"!gcreate <name> | !ginvite <nick> | !gaccept | !gdecline | " +
	"!gleave | !gkick <nick> | !ginfo [name] | !gtop"

// dispatchRegister handles !register <nick> <class…> <pass>.
// The class may span multiple words; the password is always the final token.
func dispatchRegister(src string, fields []string, g *Game, say, reply func(string)) {
	if len(fields) < 4 {
		reply("Usage: !register <nick> <class> <pass>")
		return
	}
	class := strings.Join(fields[2:len(fields)-1], " ")
	say(g.CmdRegister(src, fields[1], class, fields[len(fields)-1]))
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

// dispatchDualClass handles !dualclass <class>, where the class name may be
// multiple words joined from all remaining fields.
func dispatchDualClass(src string, fields []string, g *Game, reply func(string)) {
	if len(fields) < 2 {
		reply("Usage: !dualclass <class>")
		return
	}
	reply(g.CmdDualClass(src, strings.Join(fields[1:], " ")))
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
