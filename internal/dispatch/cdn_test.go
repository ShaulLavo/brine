package dispatch

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCDNDispatchClosesRequestsBeforeHostFactory(t *testing.T) {
	for _, input := range []struct{ op, args string }{
		{"cdn_context", `{"app":"hello","binding":"public","profile":"edge","mode":"enabled","revision":"","sample_path":"/objects/x.bin","recover_operation":""}`},
		{"cdn_apply", `{"plan_id":"sha256:` + strings.Repeat("a", 64) + `","idempotency_key":"key"}`},
		{"cdn_next", `{"operation":"op"}`},
		{"cdn_result", `{"operation":"op","result":{"kind":"uncertain","value":{"effect":"effect","code":"unknown"}}}`},
		{"cdn_status", `{"app":"hello","operation":""}`},
	} {
		checkCDNMalformedVariants(t, input.op, input.args)
	}
}

func checkCDNMalformedVariants(t *testing.T, op, args string) {
	t.Helper()
	variants := []string{
		strings.Replace(args, `"`, `"extra":true,"`, 1),
		strings.Replace(args, `"app":`, `"App":`, 1),
		strings.Replace(args, `"operation":`, `"Operation":`, 1),
		strings.Replace(args, `"effect":`, `"Effect":`, 1),
	}
	for _, malformed := range variants {
		if malformed == args {
			continue
		}
		rejectCDNBeforeFactory(t, op, malformed)
	}
}

func rejectCDNBeforeFactory(t *testing.T, op, args string) {
	t.Helper()
	s := NewServer("fixture", nil)
	s.Factory = func(context.Context, string) (*Server, error) {
		t.Fatal("malformed request reached host factory")
		return nil, nil
	}
	raw := fmt.Sprintf(`{"schema_version":1,"op":%q,"request_id":"fixture","args":%s}`, op, args)
	if _, err := s.Handle(t.Context(), bytes.NewBufferString(raw)); err == nil {
		t.Fatal("malformed CDN request accepted", op, args)
	}
}
