BINARY   := voidrift
CHANNEL  ?= \#voidrift
NICK     ?= VoidKeeper
SERVER   ?= irc.libera.chat:6667
VERSION  := $(shell date +%y%m%d)

LDFLAGS_STATIC  := -ldflags "-X main.version=$(VERSION) -extldflags=-static"
LDFLAGS_DYNAMIC := -ldflags "-X main.version=$(VERSION)"

PREFIX   ?= /usr/local
CHROOT   := /var/lib/voidrift
ENVDIR   := /etc/voidrift

.PHONY: all build build-static build-dynamic test clean run dev install uninstall

all: build

# Default build: static (no libc dependency, suitable for chroot confinement).
build: build-static

build-static:
	CGO_ENABLED=0 go build $(LDFLAGS_STATIC) -o $(BINARY) .

build-dynamic:
	CGO_ENABLED=1 go build $(LDFLAGS_DYNAMIC) -o $(BINARY) .

test:
	go test ./...

clean:
	rm -f $(BINARY)

run: build
	./$(BINARY) -server $(SERVER) -nick $(NICK) -channel "$(CHANNEL)"

dev: build
	./$(BINARY) -server $(SERVER) -nick $(NICK) -channel "$(CHANNEL)" \
		-dev -rate-player 100 -rate-align 100 -rate-server 100

# install: build a static binary, create the voidrift user and chroot
# directory, install the binary and env-file template, then wire up the
# correct init system (systemd or OpenRC/Alpine).
install: build-static
	@echo "==> Installing $(BINARY) to $(CHROOT)/$(BINARY)"
	install -dm755 $(CHROOT)
	chown root:root $(CHROOT)
	install -m755 $(BINARY) $(CHROOT)/$(BINARY)
	@echo "==> Installing env-file template to $(ENVDIR)/$(BINARY).env.example"
	install -dm700 $(ENVDIR)
	install -m600 init/$(BINARY).env.example $(ENVDIR)/$(BINARY).env.example
	@# Create the dedicated user if it does not exist yet.
	@if [ -f /etc/alpine-release ]; then \
		id -u $(BINARY) >/dev/null 2>&1 || \
			adduser -S -D -h $(CHROOT) -s /sbin/nologin $(BINARY); \
	else \
		id -u $(BINARY) >/dev/null 2>&1 || \
			useradd -r -d $(CHROOT) -s /sbin/nologin $(BINARY); \
	fi
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
	rm -f $(CHROOT)/$(BINARY)
	@echo "==> $(BINARY) uninstalled. Data in $(CHROOT) and config in $(ENVDIR) were preserved."
