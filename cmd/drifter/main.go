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
	"regexp"
	"strings"
	"time"

	irc "github.com/fluffle/goirc/client"
)

var (
	ircColorRe      = regexp.MustCompile(`\x03[0-9]{0,2}(?:,[0-9]{1,2})?`)
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
	server     := flag.String("server",       "irc.libera.chat:6667", "IRC server host:port")
	nick       := flag.String("nick",         "",  "IRC nick (required)")
	gamePass   := flag.String("game-pass",    "",  "Game password for !login (required)")
	channel    := flag.String("channel",      "#voidrift", "Channel to join")
	ssl        := flag.Bool("ssl",            false, "Use SSL")
	serverPass := flag.String("server-pass",  "",  "IRC server password")
	nickservPass := flag.String("nickserv-pass", "", "NickServ IDENTIFY password")
	logFile    := flag.String("log",          "",  "Log file path (appended; empty = stdout only)")
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

	connected := make(chan bool)

	conn.HandleFunc("connected", func(c *irc.Conn, line *irc.Line) {
		logger.Println("Connected, joining", *channel)
		if *nickservPass != "" {
			c.Privmsg("NickServ", "IDENTIFY "+*nickservPass)
		}
		c.Join(*channel)
	})

	// Send !login once we confirm our own JOIN to the channel.
	conn.HandleFunc("JOIN", func(c *irc.Conn, line *irc.Line) {
		joiningNick := line.Nick
		target := line.Args[0]
		if strings.EqualFold(joiningNick, *nick) && strings.EqualFold(target, *channel) {
			logger.Printf("Joined %s, sending !login", *channel)
			c.Privmsg(*channel, "!login "+*gamePass)
		}
	})

	conn.HandleFunc("PRIVMSG", func(c *irc.Conn, line *irc.Line) {
		target := line.Args[0]
		text := stripIRC(line.Args[1])
		logger.Printf("[%s] <%s> %s", target, line.Nick, text)
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
