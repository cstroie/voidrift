// drifter: a minimal idle IRC client for Void Drift.
// Connects to the IRC server, joins the channel, sends !login, and idles.
// All channel messages are printed to stdout and optionally written to a log file.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	irc "github.com/fluffle/goirc/client"
)

var (
	ircColorRe         = regexp.MustCompile(`\x03[0-9]{0,2}(?:,[0-9]{1,2})?`)
	ircControlReplacer = strings.NewReplacer(
		"\x02", "", "\x04", "", "\x0F", "", "\x16", "",
		"\x1D", "", "\x1E", "", "\x1F", "",
		"\r", "", "\n", "", "\x00", "",
	)
)

func stripIRC(s string) string {
	s = ircColorRe.ReplaceAllString(s, "")
	return ircControlReplacer.Replace(s)
}

func main() {
	server       := flag.String("server",       "irc.libera.chat:6667", "IRC server host:port")
	nick         := flag.String("nick",         "",           "IRC nick (required)")
	gamePass     := flag.String("game-pass",    "",           "Game password for !login (required)")
	channel      := flag.String("channel",      "#voidrift",  "Channel to join")
	ssl          := flag.Bool("ssl",            false,        "Use SSL")
	serverPass   := flag.String("server-pass",  "",           "IRC server password")
	nickservPass := flag.String("nickserv-pass", "",          "NickServ IDENTIFY password")
	botNick      := flag.String("bot",          "VoidKeeper", "Bot nick to send !login to")
	logFile      := flag.String("log",          "",           "Log file path (appended; empty = stdout only)")
	flag.Parse()

	if *nick == "" {
		fmt.Fprintln(os.Stderr, "drifter: -nick is required")
		os.Exit(1)
	}
	if *gamePass == "" {
		fmt.Fprintln(os.Stderr, "drifter: -game-pass is required")
		os.Exit(1)
	}

	// Set up logging to stdout and optionally a file.
	var logWriter io.Writer = os.Stdout
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			log.Fatalf("drifter: cannot open log file: %v", err)
		}
		defer f.Close()
		logWriter = io.MultiWriter(os.Stdout, f)
	}
	logger := log.New(logWriter, "", log.LstdFlags)

	cfg := irc.NewConfig(*nick, "drifter", "Void Drift idle client")
	cfg.SSL = *ssl
	cfg.Server = *server
	cfg.Pass = *serverPass
	cfg.NewNick = func(n string) string { return n + "_" }
	conn := irc.Client(cfg)

	// On SIGINT/SIGTERM, send !logout then exit cleanly (no quit penalty).
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		logger.Println("Shutting down, sending !logout")
		conn.Privmsg(*botNick, "!logout")
		time.Sleep(500 * time.Millisecond) // let the message flush
		os.Exit(0)
	}()

	connected := make(chan bool)

	// namesInChannel collects nicks from the NAMES reply for our channel.
	var namesInChannel []string

	// loginSent prevents sending !login more than once per session.
	var loginSent bool

	// loginAck is non-nil while we are waiting for the bot's !login reply.
	// Sending on it cancels the timeout goroutine.
	var loginAck chan struct{}

	// whoamiPending is true while we are waiting for the !whoami reply.
	var whoamiPending bool

	conn.HandleFunc("connected", func(c *irc.Conn, line *irc.Line) {
		logger.Println("Connected, joining", *channel)
		loginSent = false
		whoamiPending = false
		if *nickservPass != "" {
			c.Privmsg("NickServ", "IDENTIFY "+*nickservPass)
		}
		c.Join(*channel)
	})

	// On our own JOIN: request NAMES to verify the bot is present.
	conn.HandleFunc("JOIN", func(c *irc.Conn, line *irc.Line) {
		if !strings.EqualFold(line.Nick, *nick) {
			return
		}
		target := line.Args[0]
		if !strings.EqualFold(target, *channel) {
			return
		}
		logger.Printf("Joined %s, checking for bot %s", *channel, *botNick)
		namesInChannel = nil
		c.Raw("NAMES " + *channel)
	})

	// 353: NAMREPLY — collect nicks (strip mode prefixes like @, +, ~).
	conn.HandleFunc("353", func(c *irc.Conn, line *irc.Line) {
		// Args: [me, "=", #channel, "nick1 nick2 ..."]
		if len(line.Args) < 4 {
			return
		}
		if !strings.EqualFold(line.Args[2], *channel) {
			return
		}
		for _, n := range strings.Fields(line.Args[3]) {
			namesInChannel = append(namesInChannel, strings.TrimLeft(n, "@+~&%!"))
		}
	})

	// 366: ENDOFNAMES — bot presence check, then send !login if found.
	conn.HandleFunc("366", func(c *irc.Conn, line *irc.Line) {
		if len(line.Args) < 2 || !strings.EqualFold(line.Args[1], *channel) {
			return
		}
		if loginSent {
			return
		}
		for _, n := range namesInChannel {
			if strings.EqualFold(n, *botNick) {
				loginSent = true
				logger.Printf("Bot %s is in %s, sending !login", *botNick, *channel)
				loginAck = make(chan struct{}, 1)
				ack := loginAck
				c.Privmsg(*botNick, "!login "+*gamePass)
				go func() {
					select {
					case <-ack:
						// acknowledged — nothing to do
					case <-time.After(10 * time.Second):
						logger.Printf("WARNING: no !login reply from %s after 10s", *botNick)
					}
				}()
				return
			}
		}
		logger.Printf("WARNING: bot %s not found in %s — !login not sent; will retry on next JOIN", *botNick, *channel)
	})

	// 403: No such channel.
	conn.HandleFunc("403", func(c *irc.Conn, line *irc.Line) {
		ch := ""
		if len(line.Args) > 1 {
			ch = line.Args[1]
		}
		logger.Printf("ERROR: channel %s does not exist", ch)
	})

	// 473/474/475: Cannot join (invite-only, banned, wrong key).
	for _, num := range []string{"473", "474", "475"} {
		num := num
		conn.HandleFunc(num, func(c *irc.Conn, line *irc.Line) {
			reason := map[string]string{
				"473": "invite-only",
				"474": "banned",
				"475": "wrong channel key",
			}[num]
			logger.Printf("ERROR: cannot join %s: %s", *channel, reason)
		})
	}

	// Bot joins or parts — log for visibility.
	conn.HandleFunc("JOIN", func(c *irc.Conn, line *irc.Line) {
		if strings.EqualFold(line.Nick, *botNick) && strings.EqualFold(line.Args[0], *channel) {
			logger.Printf("Bot %s joined %s", *botNick, *channel)
		}
	})
	conn.HandleFunc("PART", func(c *irc.Conn, line *irc.Line) {
		if strings.EqualFold(line.Nick, *botNick) && strings.EqualFold(line.Args[0], *channel) {
			logger.Printf("WARNING: bot %s left %s", *botNick, *channel)
		}
	})
	conn.HandleFunc("QUIT", func(c *irc.Conn, line *irc.Line) {
		if strings.EqualFold(line.Nick, *botNick) {
			logger.Printf("WARNING: bot %s quit", *botNick)
		}
	})
	conn.HandleFunc("KICK", func(c *irc.Conn, line *irc.Line) {
		if len(line.Args) >= 2 && strings.EqualFold(line.Args[1], *botNick) {
			logger.Printf("WARNING: bot %s was kicked from %s", *botNick, *channel)
		}
	})

	conn.HandleFunc("PRIVMSG", func(c *irc.Conn, line *irc.Line) {
		target := line.Args[0]
		text := stripIRC(line.Args[1])
		logger.Printf("[%s] <%s> %s", target, line.Nick, text)

		// Watch for !whoami reply to verify we are online.
		if whoamiPending && strings.EqualFold(line.Nick, *botNick) && !strings.HasPrefix(target, "#") &&
			strings.Contains(text, "phase:") {
			whoamiPending = false
			if strings.Contains(text, "[online]") {
				logger.Printf("Online status confirmed: %s", text)
			} else {
				logger.Printf("WARNING: not online after login: %s", text)
			}
		}

		// Watch for the bot's login acknowledgement — either the private reply
		// ("logged in.") or the channel announcement ("enters the void").
		if loginAck != nil && strings.EqualFold(line.Nick, *botNick) {
			isDM      := !strings.HasPrefix(target, "#")
			isPrivAck := isDM && strings.Contains(text, "logged in.")
			isPrivErr := isDM && !strings.Contains(text, "logged in.")
			isChanAck := !isDM && strings.Contains(text, "enters the void")

			if isPrivAck || isChanAck {
				logger.Printf("Login confirmed: %s", text)
				// Verify we are online by DMing !whoami to the bot.
				// Wait 5s so any delayed private !login reply arrives first.
				go func() {
					time.Sleep(5 * time.Second)
					whoamiPending = true
					logger.Println("Sending !whoami to verify online status")
					c.Privmsg(*botNick, "!whoami")
				}()
				select {
				case loginAck <- struct{}{}:
				default:
				}
				loginAck = nil
			} else if isPrivErr {
				logger.Printf("WARNING: login failed: %s", text)
				select {
				case loginAck <- struct{}{}:
				default:
				}
				loginAck = nil
			}
		}
	})

	conn.HandleFunc("NOTICE", func(c *irc.Conn, line *irc.Line) {
		target := line.Args[0]
		text := stripIRC(line.Args[1])
		logger.Printf("[%s] -%s- %s", target, line.Nick, text)
	})

	conn.HandleFunc("disconnected", func(c *irc.Conn, line *irc.Line) {
		logger.Println("Disconnected")
		connected <- false
	})

	for {
		logger.Println("Connecting to", *server)
		if err := conn.Connect(); err != nil {
			logger.Println("Connect error:", err)
			time.Sleep(10 * time.Second)
			continue
		}
		for {
			if ok := <-connected; !ok {
				logger.Println("Reconnecting in 10s...")
				time.Sleep(10 * time.Second)
				break
			}
		}
	}
}
