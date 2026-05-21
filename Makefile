VOIDRIFT   := voidrift
DRIFTER  := drifter
CHANNEL  ?= \#voidrift
NICK     ?= VoidKeeper
SERVER   ?= irc.libera.chat:6667
VERSION  := $(shell date +%y%m%d)

LDFLAGS  := -ldflags "-X main.version=$(VERSION)"

PREFIX   ?= /usr/local
DATADIR  := /var/lib/voidrift
ENVDIR   := /etc/voidrift

.PHONY: all build test clean run dev install uninstall

all: build

build:
	go build $(LDFLAGS) -o $(VOIDRIFT) ./cmd/voidrift
	go build -o $(DRIFTER) ./cmd/drifter

test:
	go test ./...

clean:
	rm -f $(VOIDRIFT) $(DRIFTER)

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
	@echo "==> Creating data directory $(DATADIR)"
	install -dm755 $(DATADIR)
	@echo "==> Installing env-file template to $(ENVDIR)/$(VOIDRIFT).env.example"
	install -dm700 $(ENVDIR)
	install -m600 init/$(VOIDRIFT).env.example $(ENVDIR)/$(VOIDRIFT).env.example
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
		install -m755 init/$(VOIDRIFT).openrc /etc/init.d/$(VOIDRIFT); \
		rc-update add $(VOIDRIFT) default; \
		echo "==> Start with: rc-service $(VOIDRIFT) start"; \
	elif [ -d /run/systemd/system ]; then \
		echo "==> Detected systemd — installing systemd unit"; \
		install -m644 init/$(VOIDRIFT).service /etc/systemd/system/$(VOIDRIFT).service; \
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
	rm -f $(PREFIX)/bin/$(VOIDRIFT)
	@echo "==> $(VOIDRIFT) uninstalled. Data in $(DATADIR) and config in $(ENVDIR) were preserved."
