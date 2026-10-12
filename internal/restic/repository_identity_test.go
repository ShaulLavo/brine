package restic

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRepositoryConfigIdentityIsAuthenticatedClosedVersionTwo(t *testing.T) {
	id := strings.Repeat("a", 64)
	raw := []byte(`{"version":2,"id":"` + id + `","chunker_polynomial":"302dfb1905102d"}`)
	if got, err := parseRepositoryConfig(raw); err != nil || got != id {
		t.Fatal("qualified config ID refused", err)
	}
	for _, invalid := range []string{
		strings.Replace(string(raw), `"version":2`, `"version":1`, 1),
		strings.Replace(string(raw), `"version":2`, `"version":null`, 1),
		strings.Replace(string(raw), `"id":`, `"id":null,"id":`, 1),
		strings.Replace(string(raw), `"id":`, `"ID":`, 1),
		strings.Replace(string(raw), id, "arbitrary-location", 1),
		strings.Replace(string(raw), `"302dfb1905102d"`, `"UPPER"`, 1),
		strings.Replace(string(raw), `"version":`, `"credential":"private","version":`, 1),
		string(raw) + `{}`,
	} {
		if _, err := parseRepositoryConfig([]byte(invalid)); err == nil {
			t.Fatal("unknown or corrupted config identity admitted")
		}
	}
	if _, err := parseRepositoryConfig(make([]byte, 4097)); err == nil {
		t.Fatal("unbounded config admitted")
	}
}

func TestRepositoryIdentitySeparatesInitializationFromExistingScope(t *testing.T) {
	_, workspace := testTree(t)
	client, err := NewClient(strings.Repeat("a", 64), DefaultLimits(), fixtureAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	client.toolPath = workspace + "/missing-tool"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	repository := Repository{scope: testScope(), private: func() []string { t.Fatal("private material accessed before protected pin"); return nil }}
	if _, err := client.RepositoryIdentity(ctx, repository, workspace); !errors.Is(err, ErrTool) {
		t.Fatal("identity probe bypassed protected tool pin", err)
	}
	if _, err := client.InspectInitialization(ctx, repository, workspace); !errors.Is(err, ErrInvalid) {
		t.Fatal("existing scope entered initialization reconciliation", err)
	}
	repository.scope.RepositoryID = ""
	if _, err := client.RepositoryIdentity(ctx, repository, workspace); !errors.Is(err, ErrInvalid) {
		t.Fatal("unknown repository ID admitted existing operations", err)
	}
	repository.initializationPlan = strings.Repeat("b", 64)
	if _, err := client.InspectInitialization(ctx, repository, workspace); !errors.Is(err, ErrTool) {
		t.Fatal("retained creation grant bypassed protected pin", err)
	}
	point := TransportPoint{Scope: testScope()}
	repository.scope = point.Scope
	repository.initializationPlan = ""
	if !sameRepository(repository, point) {
		t.Fatal("exact repository ID mismatched")
	}
	point.Scope.RepositoryID = strings.Repeat("f", 64)
	if sameRepository(repository, point) {
		t.Fatal("same destination hash substituted another repository ID")
	}
}
