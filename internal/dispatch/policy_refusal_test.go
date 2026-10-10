package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/jobs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/spec"
)

type refusingPolicyPlanner struct{ refusal error }

func (p refusingPolicyPlanner) Plan(context.Context, spec.App) (Planned, error) {
	return Planned{}, fmt.Errorf("private-context: %w", p.refusal)
}
func (p refusingPolicyPlanner) PlanDataPreparation(ctx context.Context, a spec.App) (Planned, error) {
	return p.Plan(ctx, a)
}

func TestConnectedPolicyRefusalIsTypedAndPrivate(t *testing.T) {
	for _, op := range []string{"plan", "data_prepare_plan"} {
		for _, tc := range []struct {
			diagnostic string
			code       result.Code
		}{
			{"policy.registry_denied", result.PolicyRegistryDenied},
			{"policy.domain_denied", result.PolicyRefused},
		} {
			t.Run(op+"/"+tc.diagnostic, func(t *testing.T) {
				args, _ := json.Marshal(PlanArgs{Spec: "schema_version=1\nname=\"fixture\"\nimage=\"ghcr.io/example/fixture@sha256:" + strings.Repeat("a", 64) + "\"\ncontainer_port=8080\ndomains=[\"fixture.example.test\"]\n"})
				wire, err := EncodeRequest(Request{SchemaVersion: 1, Op: op, RequestID: "refused", Args: args})
				if err != nil {
					t.Fatal(err)
				}
				server := NewServer("test", nil)
				server.authorize = func(context.Context, Class) error { return nil }
				server.Planner = refusingPolicyPlanner{&policy.Refusal{Code: tc.diagnostic, Field: "private-field", Message: "private-value"}}
				response, err := server.Handle(t.Context(), strings.NewReader(string(wire)))
				if err == nil || result.ExitCode(err) != 4 || response.Error == nil || response.Error.Code != tc.code {
					t.Fatalf("response=%+v error=%v", response, err)
				}
				raw, _ := json.Marshal(response)
				for _, private := range []string{"private-context", "private-field", "private-value"} {
					if strings.Contains(string(raw), private) {
						t.Fatalf("leaked %s", private)
					}
				}
				if _, err := DecodeResponse(raw, op); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestCredentialAndInitializationFailureMappings(t *testing.T) {
	for _, convert := range []struct {
		name    string
		convert func(error) error
		refused error
	}{
		{"credentials", credentialFailure, backupcredentials.ErrExpired},
		{"initialization", DataInitializationFailure, datainit.ErrRefused},
	} {
		t.Run(convert.name, func(t *testing.T) {
			for _, tc := range []struct {
				err  error
				code result.Code
			}{
				{convert.refused, result.PolicyRefused},
				{errors.New("private-inventory-failure"), result.InternalError},
				{&os.PathError{Op: "read", Path: "private-policy", Err: os.ErrPermission}, result.InternalError},
				{result.New(result.DependencyMissing, errors.New("private-policy-read")), result.DependencyMissing},
				{result.New(result.PolicyRegistryDenied, convert.refused), result.PolicyRegistryDenied},
				{&policy.Refusal{Code: "policy.registry_denied", Field: "private", Message: "private"}, result.PolicyRegistryDenied},
				{context.Canceled, result.Interrupted},
			} {
				err := convert.convert(fmt.Errorf("private-wrapper: %w", tc.err))
				if result.Classify(err).Code() != tc.code {
					t.Fatalf("%v mapped to %s, want %s", tc.err, result.Classify(err).Code(), tc.code)
				}
				raw, _ := json.Marshal(result.Failure("brine", err))
				if strings.Contains(string(raw), "private") {
					t.Fatal("private failure details leaked")
				}
			}
			if convert.convert(nil) != nil {
				t.Fatal("nil failure is not success")
			}
		})
	}
}

type failingCredentialOperations struct{ err error }

func (f failingCredentialOperations) Plan(context.Context, string, string, *time.Time) (backupcredentials.Plan, error) {
	return backupcredentials.Plan{}, f.err
}
func (f failingCredentialOperations) Set(context.Context, string, string, string, backupcredentials.Packet) (jobs.Accepted, error) {
	return jobs.Accepted{}, f.err
}

type failingInitializationOperations struct{ err error }

func (f failingInitializationOperations) Plan(context.Context, datainit.Request) (datainit.Plan, error) {
	return datainit.Plan{}, f.err
}
func (f failingInitializationOperations) Apply(context.Context, string, string) (jobs.Accepted, error) {
	return jobs.Accepted{}, f.err
}
func (f failingInitializationOperations) Operation(context.Context, string, uint64) (jobs.Status, error) {
	return jobs.Status{}, f.err
}

func TestOperationDispatchPreservesOperationalAndTypedErrors(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	for _, op := range []string{"backup_credentials_plan", "data_init_plan", "data_init_apply"} {
		for _, tc := range []struct {
			err  error
			code result.Code
		}{
			{errors.New("private-inventory-failure"), result.InternalError},
			{&os.PathError{Op: "read", Path: "private-policy", Err: os.ErrPermission}, result.InternalError},
			{result.New(result.DependencyMissing, nil), result.DependencyMissing},
			{&policy.Refusal{Code: "policy.registry_denied", Field: "private", Message: "private"}, result.PolicyRegistryDenied},
		} {
			t.Run(op+"/"+string(tc.code), func(t *testing.T) {
				args := `{"app":"fixture","database":"main","expires_at":null}`
				if op == "data_init_plan" {
					args = `{"app":"fixture","first_release_plan":"` + hash + `","artifact":"` + hash + `"}`
				}
				if op == "data_init_apply" {
					args = `{"app":"fixture","plan_id":"` + hash + `"}`
				}
				wire, err := EncodeRequest(Request{SchemaVersion: 1, Op: op, RequestID: "failure", Args: json.RawMessage(args)})
				if err != nil {
					t.Fatal(err)
				}
				s := NewServer("test", nil)
				s.authorize = func(context.Context, Class) error { return nil }
				s.BackupCredentials = failingCredentialOperations{tc.err}
				s.DataInitialization = failingInitializationOperations{tc.err}
				response, err := s.Handle(t.Context(), strings.NewReader(string(wire)))
				if err == nil || response.Error == nil || response.Error.Code != tc.code {
					t.Fatalf("response=%+v err=%v", response, err)
				}
				raw, _ := json.Marshal(response)
				if strings.Contains(string(raw), "private") {
					t.Fatal("private detail leaked")
				}
				if _, err := DecodeResponse(raw, op); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
