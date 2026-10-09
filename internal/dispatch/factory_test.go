package dispatch

import (
	"context"
	"github.com/ShaulLavo/brine/internal/result"
	"strings"
	"testing"
)

func TestFactoryRunsAfterRequestValidation(t *testing.T) {
	calls := 0
	server := NewServer("fixture", nil)
	server.Factory = func(context.Context, string) (*Server, error) {
		calls++
		return nil, result.New(result.DependencyMissing, nil)
	}
	if _, err := server.Handle(context.Background(), strings.NewReader(`{"op":"apply"}`)); err == nil || calls != 0 {
		t.Fatal("initialized before request validation")
	}
	if _, err := server.Handle(context.Background(), strings.NewReader(`{"schema_version":1,"op":"ping","request_id":"fixture","args":{}}`)); err == nil || calls != 1 {
		t.Fatal("missing factory error")
	}
}
