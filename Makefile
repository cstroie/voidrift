BINARY   := voidrift
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
	go build $(LDFLAGS) -o $(BINARY) ./cmd/voidrift

test:
	go test ./...

clean:
	rm -f $(BINARY)

run: build
	./$(BINARY) -server $(SERVER) -nick $(NICK) -channel "$(CHANNEL)"

dev: build
	./$(BINARY) -server $(SERVER) -nick $(NICK) -channel "$(CHANNEL)" \
		-dev -rate-player 100 -rate-align 100 -rate-server 100

# install: build and install the binary to PREFIX/bin, create the data and
# env directories, create the voidrift user, then wire up the correct init
# system (systemd or OpenRC/Alpine).
install: build
	@echo "==> Installing $(BINARY) to $(PREFIX)/bin/$(BINARY)"
	install -dm755 $(PREFIX)/bin
	install -m755 $(BINARY) $(PREFIX)/bin/$(BINARY)
	@echo "==> Creating data directory $(DATADIR)"
	install -dm755 $(DATADIR)
	@echo "==> Installing env-file template to $(ENVDIR)/$(BINARY).env.example"
	install -dm700 $(ENVDIR)
	install -m600 init/$(BINARY).env.example $(ENVDIR)/$(BINARY).env.example
	@# Create the dedicated user if it does not exist yet.
	@if [ -f /etc/alpine-release ]; then \
		id -u $(BINARY) >/dev/null 2>&1 || \
			adduser -S -D -h $(DATADIR) -s /sbin/nologin $(BINARY); \
	else \
		id -u $(BINARY) >/dev/null 2>&1 || \
			useradd -r -d $(DATADIR) -s /sbin/nologin $(BINARY); \
	fi
	chown voidrift $(DATADIR)
	@# Install the appropriate init file.
	@if [ -f /etc/alpine-release ]; then \
		echo "==> Detected Alpine Linux — installing OpenRC service"; \
		install -m755 init/$(BINARY).openrc /etc/init.d/$(BINARY); \
		rc-update add $(BINARY) default; \
		echo "==> Start with: rc-service $(BINARY) start"; \
	elif [ -d /run/systemd/system ]; then \
		echo "==> Detected systemd — installing systemd unit"; \
		install -m644 init/$(BINARY).service /etc/systemd/system/$(BINARY).service; \
		systemctl daemon-reload; \
		systemctl enable $(BINARY); \
		echo "==> Start with: systemctl start $(BINARY)"; \
	else \
		echo "WARNING: could not detect init system; init file not installed."; \
		echo "  Manually install init/$(BINARY).service or init/$(BINARY).openrc."; \
	fi
	@echo "==> Done. Copy $(ENVDIR)/$(BINARY).env.example to $(ENVDIR)/$(BINARY).env and set your config."

uninstall:
	@if [ -f /etc/alpine-release ]; then \
		rc-service $(BINARY) stop 2>/dev/null || true; \
		rc-update del $(BINARY) 2>/dev/null || true; \
		rm -f /etc/init.d/$(BINARY); \
	elif [ -d /run/systemd/system ]; then \
		systemctl stop $(BINARY) 2>/dev/null || true; \
		systemctl disable $(BINARY) 2>/dev/null || true; \
		rm -f /etc/systemd/system/$(BINARY).service; \
		systemctl daemon-reload; \
	fi
	rm -f $(PREFIX)/bin/$(BINARY)
	@echo "==> $(BINARY) uninstalled. Data in $(DATADIR) and config in $(ENVDIR) were preserved."
