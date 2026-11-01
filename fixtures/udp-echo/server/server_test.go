package server

import (
	"net"
	"testing"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
)

func TestInvalidConfigurationDoesNotBind(t *testing.T) {
	for _, address := range []string{"localhost:7777", "127.0.0.1:80", "[fe80::1%lo]:7777", "224.0.0.1:7777", "127.0.0.1:bad"} {
		if err := validateBind(address); err == nil {
			t.Fatalf("invalid bind accepted: %s", address)
		}
	}
	fixture, err := New(t.Context(), Config{HTTPAddr: "127.0.0.1:0", UDPAddr: "127.0.0.1:0", Identity: protocol.Identity{Fixture: protocol.FixtureName}})
	if err == nil {
		_ = fixture.Close(t.Context())
		t.Fatal("empty release accepted")
	}
}

func TestUDPBindFailureClosesHTTP(t *testing.T) {
	httpListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpAddress := httpListener.Addr().String()
	_ = httpListener.Close()
	udpListener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = udpListener.Close() }()
	fixture, err := New(t.Context(), Config{HTTPAddr: httpAddress, UDPAddr: udpListener.LocalAddr().String(), Identity: protocol.Identity{Fixture: protocol.FixtureName, Release: "test"}})
	if err == nil {
		_ = fixture.Close(t.Context())
		t.Fatal("occupied UDP address accepted")
	}
	httpListener, err = net.Listen("tcp", httpAddress)
	if err != nil {
		t.Fatalf("failed startup retained HTTP listener: %v", err)
	}
	_ = httpListener.Close()
}
