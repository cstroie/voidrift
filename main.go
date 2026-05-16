package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	irc "github.com/fluffle/goirc/client"
)

func main() {
	server   := flag.String("server", "irc.libera.chat:6667", "IRC server host:port")
	nick     := flag.String("nick", "idlerpgbot", "Bot nick")
	password := flag.String("password", "", "Server password")
	ssl      := flag.Bool("ssl", false, "Use SSL")
	channel  := flag.String("channel", "#idlerpg", "Game channel")
	dataFile   := flag.String("data", "idlerpg.json", "Player data file")
	guildsFile := flag.String("guilds", "guilds.json", "Guild data file")
	flag.Parse()

	cfg := irc.NewConfig(*nick, "idlerpg", "IdleRPG bot")
	cfg.SSL = *ssl
	cfg.Server = *server
	cfg.Pass = *password
	cfg.NewNick = func(n string) string { return n + "_" }

	conn := irc.Client(cfg)

	// say sends a message to the game channel.
	say := func(msg string) {
		conn.Privmsg(*channel, msg)
	}

	game := newGame(*dataFile, *guildsFile, say)

	conn.HandleFunc("connected", func(c *irc.Conn, line *irc.Line) {
		log.Println("Connected, joining", *channel)
		c.Join(*channel)
		game.start()
	})

	conn.HandleFunc("JOIN", func(c *irc.Conn, line *irc.Line) {
		// Ignore the bot's own joins.
		if extractNick(line.Src) == *nick {
			return
		}
		game.OnJoin(line.Src)
	})

	conn.HandleFunc("PART", func(c *irc.Conn, line *irc.Line) {
		game.OnPart(line.Src)
	})

	conn.HandleFunc("QUIT", func(c *irc.Conn, line *irc.Line) {
		game.OnQuit(line.Src)
	})

	conn.HandleFunc("NICK", func(c *irc.Conn, line *irc.Line) {
		game.OnNick(line.Src, line.Args[0])
	})

	conn.HandleFunc("KICK", func(c *irc.Conn, line *irc.Line) {
		if len(line.Args) < 2 {
			return
		}
		kicked := line.Args[1]
		if kicked == *nick {
			// Bot was kicked — rejoin.
			c.Join(*channel)
			return
		}
		game.OnKick(kicked)
	})

	conn.HandleFunc("PRIVMSG", func(c *irc.Conn, line *irc.Line) {
		if len(line.Args) < 2 {
			return
		}
		ch := line.Args[0]
		text := strings.TrimSpace(line.Args[1])
		src := line.Src

		// Route PM replies back to the sender's nick.
		replyTo := ch
		if ch == *nick {
			replyTo = extractNick(src)
		}

		reply := func(msg string) { conn.Privmsg(replyTo, msg) }

		fields := strings.Fields(text)
		if len(fields) == 0 {
			return
		}

		switch fields[0] {
		case "!register":
			if len(fields) < 4 {
				reply("Usage: !register <nick> <class> <pass>")
				return
			}
			// class may be multiple words; password is always last field
			class := strings.Join(fields[2:len(fields)-1], " ")
			pass := fields[len(fields)-1]
			msg := game.CmdRegister(src, fields[1], class, pass)
			say(msg)

		case "!login":
			if len(fields) < 2 {
				reply("Usage: !login <pass>")
				return
			}
			msg := game.CmdLogin(src, fields[1])
			say(msg)

		case "!logout":
			msg := game.CmdLogout(src)
			reply(msg)

		case "!gcreate":
			if len(fields) < 2 {
				reply("Usage: !gcreate <name>")
				return
			}
			name := strings.Join(fields[1:], " ")
			say(game.CmdGCreate(src, name))

		case "!ginvite":
			if len(fields) < 2 {
				reply("Usage: !ginvite <nick>")
				return
			}
			reply(game.CmdGInvite(src, fields[1]))

		case "!gaccept":
			say(game.CmdGAccept(src))

		case "!gdecline":
			reply(game.CmdGDecline(src))

		case "!gleave":
			say(game.CmdGLeave(src))

		case "!gkick":
			if len(fields) < 2 {
				reply("Usage: !gkick <nick>")
				return
			}
			say(game.CmdGKick(src, fields[1]))

		case "!ginfo":
			name := ""
			if len(fields) >= 2 {
				name = strings.Join(fields[1:], " ")
			}
			reply(game.CmdGInfo(src, name))

		case "!gtop":
			reply(game.CmdGTop())

		case "!align":
			if len(fields) < 2 {
				reply("Usage: !align <good|neutral|evil>")
				return
			}
			reply(game.CmdAlign(src, fields[1]))

		case "!status":
			target := ""
			if len(fields) >= 2 {
				target = fields[1]
			}
			reply(game.CmdStatus(src, target))

		case "!whoami":
			reply(game.CmdStatus(src, ""))

		case "!top":
			reply(game.CmdTop())

		case "!pos":
			target := ""
			if len(fields) >= 2 {
				target = fields[1]
			}
			reply(game.CmdPos(src, target))

		case "!help":
			reply("IdleRPG commands: " +
				"!register <nick> <class> <pass> | " +
				"!login <pass> | !logout | " +
				"!align <good|neutral|evil> | " +
				"!status [nick] | !whoami | !top | !pos [nick] | " +
				"!gcreate <name> | !ginvite <nick> | !gaccept | !gdecline | " +
				"!gleave | !gkick <nick> | !ginfo [name] | !gtop")

		default:
			// Penalize online players for talking in channel (not PMs, not commands).
			if ch == *channel && !strings.HasPrefix(text, "!") {
				game.OnPrivmsg(src, text)
			}
		}
	})

	// Reconnect loop.
	connected := make(chan bool)
	conn.HandleFunc("disconnected", func(c *irc.Conn, line *irc.Line) {
		connected <- false
	})

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

func init() {
	fmt.Println("IdleRPG bot starting")
}
