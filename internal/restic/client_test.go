package restic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
)

func testScope() Scope {
	return Scope{BindingID: testIdentity().BindingID, EpochID: testIdentity().EpochID, RepositoryID: strings.Repeat("e", 64), DestinationSHA256: strings.Repeat("f", 64), CredentialVersion: 1, RecoveryVersion: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)}
}

type fixtureAdmission struct{}

func (fixtureAdmission) AdmitRepository(_ context.Context, scope Scope, action RepositoryAction, creationPlan string) error {
	if !scope.valid() || action == CreateRepository && !hashPattern.MatchString(creationPlan) {
		return ErrInvalid
	}
	return nil
}

func TestRepositoryRedactionScopeAndExpiry(t *testing.T) {
	location := S3Location{Endpoint: "https://storage.example.invalid", Bucket: "fixture-bucket", Prefix: "fixture/prefix", Region: "fixture-region", PathStyle: true}
	packet, err := backupcredentials.DecodePacket([]byte(`{"access_key_id":"fixture-access","secret_access_key":"fixture-secret","session_token":"fixture-session"}`))
	if err != nil {
		t.Fatal(err)
	}
	scope := testScope()
	scope.DestinationSHA256 = location.SHA256()
	repository, err := NewS3Repository(scope, location, []byte("fixture-recovery-password"), packet, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{fmt.Sprintf("%+v", repository), fmt.Sprintf("%#v", repository), fmt.Sprintf("%s", repository)} {
		if strings.Contains(output, "fixture-") || strings.Contains(output, "example.invalid") {
			t.Fatal("private repository escaped formatting")
		}
	}
	env := repository.private()
	if !strings.Contains(strings.Join(env, "\n"), "AWS_SESSION_TOKEN=fixture-session") {
		t.Fatal("session token missing from private environment")
	}
	env[0] = "modified"
	if repository.private()[0] == "modified" {
		t.Fatal("private environment aliases caller")
	}
	repository.scope.ExpiresAt = time.Now().UTC()
	if err := repository.admit(time.Now().UTC().Add(time.Minute), testIdentity()); !errors.Is(err, ErrExpired) {
		t.Fatal(err)
	}
	scope.DestinationSHA256 = strings.Repeat("a", 64)
	if _, err := NewS3Repository(scope, location, []byte("fixture-recovery-password"), packet, ""); !errors.Is(err, ErrInvalid) {
		t.Fatal("destination mismatch admitted")
	}
}

func TestProtectedPinRefusesBeforeRepositoryUse(t *testing.T) {
	_, work := testTree(t)
	client, err := NewClient(strings.Repeat("a", 64), DefaultLimits(), fixtureAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	client.toolPath = filepath.Join(work, "missing-tool")
	called := false
	repository := Repository{scope: testScope(), private: func() []string { called = true; return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := client.Check(ctx, repository, work); !errors.Is(err, ErrTool) || called {
		t.Fatal("secret accessor ran before protected pin")
	}
}

func TestResticLocalEncryptedRoundTrip(t *testing.T) {
	if os.Getenv("BRINE_RESTIC_GATE") != "1" {
		t.Skip("explicit disposable local restic gate")
	}
	sha := os.Getenv("BRINE_RESTIC_GATE_SHA256")
	client, err := NewClient(sha, DefaultLimits(), fixtureAdmission{})
	if err != nil {
		t.Fatal("gate requires the reviewed installed binary SHA-256")
	}
	client.toolPath = "/usr/bin/restic"
	source, work := testTree(t)
	base := filepath.Dir(source)
	repositoryPath := filepath.Join(base, "repository")
	private := []string{"RESTIC_REPOSITORY=" + repositoryPath, "RESTIC_PASSWORD=disposable-fixture-recovery-password"}
	repository := Repository{scope: testScope(), initializationPlan: strings.Repeat("a", 64), private: func() []string { return append([]string{}, private...) }}
	repository.scope.RepositoryID = ""
	t.Setenv("RESTIC_PASSWORD", "ambient-password-must-not-be-used")
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient-key-must-not-be-used")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	repository, err = client.Init(ctx, repository, work)
	if err != nil {
		t.Fatal("actual encrypted initialization", err)
	}
	bundle, err := Seal(ctx, source, work, testIdentity(), DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = bundle.Close() }()
	point, err := client.Backup(ctx, repository, work, bundle)
	if err != nil {
		t.Fatal("actual encrypted backup", err)
	}
	if err := client.InspectExact(ctx, repository, work, point); err != nil {
		t.Fatal("actual exact inspection", err)
	}
	adopted, err := client.InspectCapture(ctx, repository, work, bundle)
	if err != nil || adopted != point {
		t.Fatal("actual interrupted-capture inspection", err)
	}
	if err := client.Check(ctx, repository, work); err != nil {
		t.Fatal("actual repository data check", err)
	}
	output := filepath.Join(work, "remote-restore")
	if err := client.RestoreExact(ctx, repository, work, output, point, bundle.Manifest()); err != nil {
		t.Fatal("actual exact remote download and member verification", err)
	}
	// #nosec G304 -- Exact member in disposable local fixture source.
	want, err := os.ReadFile(filepath.Join(source, "state"))
	if err != nil {
		t.Fatal(err)
	}
	// #nosec G304 -- Exact member in disposable local fixture restoration.
	got, err := os.ReadFile(filepath.Join(output, "state"))
	if err != nil || !bytes.Equal(want, got) {
		t.Fatal("actual restored member differs")
	}
	wrong := point
	wrong.ArchiveSHA256 = strings.Repeat("0", 64)
	if err := client.RestoreExact(ctx, repository, work, filepath.Join(work, "bad-restore"), wrong, bundle.Manifest()); !errors.Is(err, ErrIntegrity) {
		t.Fatal("actual wrong archive digest admitted", err)
	}
	if _, err := os.Stat(filepath.Join(work, "bad-restore")); !os.IsNotExist(err) {
		t.Fatal("integrity refusal extracted bytes")
	}
	if _, err := client.Backup(ctx, repository, work, bundle); err != nil {
		t.Fatal("second exact capture", err)
	}
	if _, err := client.InspectCapture(ctx, repository, work, bundle); !errors.Is(err, ErrIntegrity) {
		t.Fatal("duplicate capture snapshots admitted", err)
	}
	t.Logf("restic=%s tool_sha256=%s archive_bytes=%d manifest_sha256=%s exact_snapshot=%s local_encrypted_members=verified", Version, sha, point.ArchiveBytes, point.ManifestSHA256, point.SnapshotID)
}
