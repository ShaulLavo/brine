package main

import (
	"context"
	"errors"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
	"github.com/ShaulLavo/brine/fixtures/udp-echo/server"
)

func main() {
	config, err := configFromEnv()
	if err != nil {
		log.Print(err)
		os.Exit(2)
	}
	fixture, err := server.New(config)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	defer func() { _ = fixture.Close() }()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := fixture.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Print(err)
		os.Exit(1)
	}
}

func configFromEnv() (server.Config, error) {
	identity := protocol.Identity{Fixture: protocol.FixtureName, Release: os.Getenv("BRINE_UDP_ECHO_RELEASE")}
	if identity.Release == "" {
		identity.Release = "udp-echo-v1"
	}
	return server.Config{HTTPAddr: envOr("BRINE_UDP_ECHO_HTTP_ADDR", server.DefaultHTTPAddr), UDPAddr: envOr("BRINE_UDP_ECHO_UDP_ADDR", server.DefaultUDPAddr), Identity: identity}, nil
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
