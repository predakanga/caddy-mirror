.PHONY: all

all: caddy

caddy: go.mod go.sum mirror.go
	xcaddy build --with github.com/predakanga/caddy-mirror=$(PWD)

caddy-dbg: go.mod go.sum mirror.go
	XCADDY_DEBUG=1 xcaddy build --output caddy-dbg --with github.com/predakanga/caddy-mirror=$(PWD)