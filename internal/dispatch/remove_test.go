package dispatch

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/result"
)

type removeConfig struct{ calls int }

func (*removeConfig) ConfigSet(context.Context, string, []apps.Edit) (apps.ConfigPlan, error) {
	panic("unexpected config")
}
func (s *removeConfig) Lifecycle(_ context.Context, app string, action plan.ChangeKind) (apps.ConfigPlan, error) {
	s.calls++
	return apps.ConfigPlan{PlanID: "sha256:" + strings.Repeat("a", 64), Kind: plan.Update, Lifecycle: action, Conflicts: []plan.Diagnostic{}}, nil
}
func TestD8RemoveUsesMutatingAuthorizationGate(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		config := &removeConfig{}
		server := NewServer("fixture", nil)
		server.Config = config
		gates := 0
		server.authorize = func(_ context.Context, class Class) error {
			gates++
			if class != Mutating {
				t.Fatal("remove classified read-only")
			}
			if !authorized {
				return result.New(result.DispatchOperationRefused, nil)
			}
			return nil
		}
		request, err := EncodeRequest(Request{SchemaVersion: 1, Op: "lifecycle", RequestID: "remove", Args: json.RawMessage(`{"app":"hello","action":"remove_app"}`)})
		if err != nil {
			t.Fatal(err)
		}
		response, err := server.Handle(context.Background(), bytes.NewReader(request))
		if authorized && (err != nil || !response.OK || config.calls != 1) || !authorized && (result.Classify(err).Code() != result.DispatchOperationRefused || config.calls != 0) || gates != 1 {
			t.Fatal(authorized, response, err, config.calls, gates)
		}
	}
	for _, args := range []string{`{"app":"../other","action":"remove_app"}`, `{"app":"hello","action":"remove_app","purge":true}`, `{"app":"hello","action":"remove_app","unit":"other.service"}`} {
		_, err := EncodeRequest(Request{SchemaVersion: 1, Op: "lifecycle", RequestID: "remove", Args: json.RawMessage(args)})
		if err == nil {
			t.Fatal("expanded remove scope", args)
		}
	}
}
