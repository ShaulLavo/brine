package main

import "testing"

func TestInvalidEnvironmentRefusesBeforeBinding(t *testing.T) {
	t.Setenv("UDP_ECHO_FAIL_HEALTH", "invalid")
	if err := run(); err == nil {
		t.Fatal("invalid health mode accepted")
	}
	t.Setenv("UDP_ECHO_FAIL_HEALTH", "false")
	t.Setenv("UDP_ECHO_HTTP_ADDR", "localhost:8080")
	if err := run(); err == nil {
		t.Fatal("nonliteral bind accepted")
	}
}
