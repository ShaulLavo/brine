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

var release = "udp-echo-v1"

func main() {
	if err := run(); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run() error {
	failHealth := os.Getenv("UDP_ECHO_FAIL_HEALTH")
	if failHealth != "" && failHealth != "true" && failHealth != "false" {
		return errors.New("UDP_ECHO_FAIL_HEALTH requires true or false")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	fixture, err := server.New(ctx, server.Config{
		HTTPAddr:   envOr("UDP_ECHO_HTTP_ADDR", server.DefaultHTTPAddr),
		UDPAddr:    envOr("UDP_ECHO_UDP_ADDR", server.DefaultUDPAddr),
		Identity:   protocol.Identity{Fixture: protocol.FixtureName, Release: release},
		FailHealth: failHealth == "true",
	})
	if err != nil {
		return err
	}
	defer func() { _ = fixture.Close(ctx) }()
	log.Printf("fixture %s release %s bound HTTP %s and UDP %s", protocol.FixtureName, release, fixture.HTTPAddr(), fixture.UDPAddr())
	return fixture.Run(ctx)
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
