VOIDRIFT   := voidrift
DRIFTER  := drifter
CHANNEL  ?= \#voidrift
NICK     ?= VoidKeeper
SERVER   ?= irc.libera.chat:6667
VERSION  := $(shell date +%y%m%d)

LDFLAGS  := -ldflags "-X main.version=$(VERSION)"

PREFIX   ?= /usr
MANDIR   ?= $(PREFIX)/share/man
DATADIR  := /var/lib/voidrift
ENVDIR   := /etc/voidrift

PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

.PHONY: all build dist $(PLATFORMS) test clean run dev install uninstall

all: build

build:
	go build $(LDFLAGS) -o $(VOIDRIFT) ./cmd/voidrift
	go build $(LDFLAGS) -o $(DRIFTER) ./cmd/drifter

# Build for all platforms: make dist
dist: $(PLATFORMS)

linux/amd64:
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o $(VOIDRIFT)-linux-amd64   ./cmd/voidrift
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o $(DRIFTER)-linux-amd64    ./cmd/drifter

linux/arm64:
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o $(VOIDRIFT)-linux-arm64   ./cmd/voidrift
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o $(DRIFTER)-linux-arm64    ./cmd/drifter

darwin/amd64:
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o $(VOIDRIFT)-darwin-amd64  ./cmd/voidrift
	GOOS=darwin  GOARCH=amd64 go build $(LDFLAGS) -o $(DRIFTER)-darwin-amd64   ./cmd/drifter

darwin/arm64:
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o $(VOIDRIFT)-darwin-arm64  ./cmd/voidrift
	GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o $(DRIFTER)-darwin-arm64   ./cmd/drifter

windows/amd64:
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(VOIDRIFT)-windows-amd64.exe ./cmd/voidrift
	GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(DRIFTER)-windows-amd64.exe  ./cmd/drifter

test:
	go test ./...

clean:
	rm -f $(VOIDRIFT) $(DRIFTER) \
		$(VOIDRIFT)-linux-amd64   $(DRIFTER)-linux-amd64 \
		$(VOIDRIFT)-linux-arm64   $(DRIFTER)-linux-arm64 \
		$(VOIDRIFT)-darwin-amd64  $(DRIFTER)-darwin-amd64 \
		$(VOIDRIFT)-darwin-arm64  $(DRIFTER)-darwin-arm64 \
		$(VOIDRIFT)-windows-amd64.exe $(DRIFTER)-windows-amd64.exe

man: man/man1/voidrift.1 man/man1/drifter.1

run: build
	./$(VOIDRIFT) -server $(SERVER) -nick $(NICK) -channel "$(CHANNEL)"

dev: build
	./$(VOIDRIFT) -server $(SERVER) -nick $(NICK) -channel "$(CHANNEL)" \
		-dev -rate-player 100 -rate-align 100 -rate-server 100

# install: build and install the binary to PREFIX/bin, create the data and
# env directories, create the voidrift user, then wire up the correct init
# system (systemd or OpenRC/Alpine).
install: build
	@echo "==> Installing $(VOIDRIFT) to $(PREFIX)/bin/$(VOIDRIFT)"
	install -dm755 $(PREFIX)/bin
	install -m755 $(VOIDRIFT) $(PREFIX)/bin/$(VOIDRIFT)
	install -m755 $(DRIFTER) $(PREFIX)/bin/$(DRIFTER)
	@echo "==> Installing man pages to $(MANDIR)/man1"
	install -dm755 $(MANDIR)/man1
	install -m644 man/man1/voidrift.1 $(MANDIR)/man1/voidrift.1
	install -m644 man/man1/drifter.1 $(MANDIR)/man1/drifter.1
	@echo "==> Creating data directory $(DATADIR)"
	install -dm755 $(DATADIR)
	@echo "==> Installing env-file template to $(ENVDIR)/$(VOIDRIFT).env.example"
	install -dm700 $(ENVDIR)
	install -m600 init/$(VOIDRIFT).env.example $(ENVDIR)/$(VOIDRIFT).env.example
	@if [ -d /etc/logrotate.d ]; then install -m644 init/$(VOIDRIFT).logrotate /etc/logrotate.d/$(VOIDRIFT); fi
	@# Create the dedicated user if it does not exist yet.
	@if [ -f /etc/alpine-release ]; then \
		id -u $(VOIDRIFT) >/dev/null 2>&1 || \
			adduser -S -D -h $(DATADIR) -s /sbin/nologin $(VOIDRIFT); \
	else \
		id -u $(VOIDRIFT) >/dev/null 2>&1 || \
			useradd -r -d $(DATADIR) -s /sbin/nologin $(VOIDRIFT); \
	fi
	chown voidrift $(DATADIR)
	@# Install the appropriate init file.
	@if [ -f /etc/alpine-release ]; then \
		echo "==> Detected Alpine Linux — installing OpenRC service"; \
		sed "s|/usr/bin/voidrift|$(PREFIX)/bin/$(VOIDRIFT)|" init/$(VOIDRIFT).openrc > /etc/init.d/$(VOIDRIFT); chmod 755 /etc/init.d/$(VOIDRIFT); \
		rc-update add $(VOIDRIFT) default; \
		echo "==> Start with: rc-service $(VOIDRIFT) start"; \
	elif [ -d /run/systemd/system ]; then \
		echo "==> Detected systemd — installing systemd unit"; \
		sed "s|/usr/bin/voidrift|$(PREFIX)/bin/$(VOIDRIFT)|" init/$(VOIDRIFT).service > /etc/systemd/system/$(VOIDRIFT).service; chmod 644 /etc/systemd/system/$(VOIDRIFT).service; \
		systemctl daemon-reload; \
		systemctl enable $(VOIDRIFT); \
		echo "==> Start with: systemctl start $(VOIDRIFT)"; \
	else \
		echo "WARNING: could not detect init system; init file not installed."; \
		echo "  Manually install init/$(VOIDRIFT).service or init/$(VOIDRIFT).openrc."; \
	fi
	@echo "==> Done. Copy $(ENVDIR)/$(VOIDRIFT).env.example to $(ENVDIR)/$(VOIDRIFT).env and set your config."

uninstall:
	@if [ -f /etc/alpine-release ]; then \
		rc-service $(VOIDRIFT) stop 2>/dev/null || true; \
		rc-update del $(VOIDRIFT) 2>/dev/null || true; \
		rm -f /etc/init.d/$(VOIDRIFT); \
	elif [ -d /run/systemd/system ]; then \
		systemctl stop $(VOIDRIFT) 2>/dev/null || true; \
		systemctl disable $(VOIDRIFT) 2>/dev/null || true; \
		rm -f /etc/systemd/system/$(VOIDRIFT).service; \
		systemctl daemon-reload; \
	fi
	rm -f $(PREFIX)/bin/$(VOIDRIFT) $(PREFIX)/bin/$(DRIFTER) /etc/logrotate.d/$(VOIDRIFT)
	rm -f $(MANDIR)/man1/voidrift.1 $(MANDIR)/man1/drifter.1
	@echo "==> $(VOIDRIFT) uninstalled. Data in $(DATADIR) and config in $(ENVDIR) were preserved."
