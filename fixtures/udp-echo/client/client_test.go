package client

import (
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/fixtures/udp-echo/protocol"
)

func TestProbeConfigBounds(t *testing.T) {
	base := Config{Endpoint: "127.0.0.1:7777", Nonce: "nonce", Vantage: "local", ExpectedRelease: "release-a", Attempts: 1, AttemptTimeout: time.Second, OverallTimeout: time.Second}
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{name: "attempts", change: func(config *Config) { config.Attempts = protocol.MaxAttempts + 1 }},
		{name: "attempt timeout", change: func(config *Config) { config.AttemptTimeout = protocol.MaxTimeoutSeconds*time.Second + time.Nanosecond }},
		{name: "overall timeout", change: func(config *Config) { config.OverallTimeout = protocol.MaxOverallSeconds*time.Second + time.Nanosecond }},
		{name: "nonce", change: func(config *Config) { config.Nonce = strings.Repeat("a", protocol.MaxNonceBytes+1) }},
		{name: "vantage", change: func(config *Config) { config.Vantage = strings.Repeat("a", protocol.MaxVantageBytes+1) }},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.change(&config)
			result, err := Probe(t.Context(), config)
			if err == nil || result.Outcome != "configuration_error" {
				t.Fatalf("bounds accepted: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestMarshalResultRejectsOversizedJSON(t *testing.T) {
	_, err := MarshalResult(Result{Schema: protocol.SchemaVersion, Fixture: protocol.FixtureName, Error: strings.Repeat("a", protocol.MaxHTTPBodyBytes)})
	if err == nil {
		t.Fatal("oversized result accepted")
	}
}
