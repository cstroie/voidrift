BINARY  := voidrift
CHANNEL ?= \#voidrift
NICK    ?= VoidKeeper
SERVER  ?= irc.libera.chat:6667
VERSION := $(shell date +%y%m%d)
LDFLAGS := -ldflags "-X main.version=$(VERSION) -extldflags=-static"

# CGO_ENABLED=0 produces a fully static binary with no libc dependency,
# which is required for chroot confinement (no shared libs inside the chroot).
export CGO_ENABLED=0

.PHONY: all build test clean run dev

all: build

build:
	go build $(LDFLAGS) -o $(BINARY) .

test:
	go test ./...

clean:
	rm -f $(BINARY)

run: build
	./$(BINARY) -server $(SERVER) -nick $(NICK) -channel "$(CHANNEL)"

dev: build
	./$(BINARY) -server $(SERVER) -nick $(NICK) -channel "$(CHANNEL)" \
		-dev -rate-player 100 -rate-align 100 -rate-server 100
