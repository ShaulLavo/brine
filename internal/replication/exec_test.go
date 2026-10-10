//go:build linux

package replication

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type launchReader struct {
	permitReader
	environment   []string
	credentialErr error
	onRead        func(int, *PermitState)
}

func (r *launchReader) ReadReplicaPermit(ctx context.Context, id string) (PermitState, error) {
	r.reads++
	if r.onRead != nil {
		r.onRead(r.reads, &r.state)
	}
	return r.state, r.err
}

func (r *launchReader) ReadCredentialEnvironment(context.Context, string) ([]string, error) {
	return r.environment, r.credentialErr
}
func TestReplicaExecHoldsLockAndDropsInheritedProviders(t *testing.T) {
	s := allowedState(t)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	s.LifetimeLock = filepath.Join(dir, "binding.lock")
	s.ConfigPath = "/srv/state/replication/" + s.Binding.BindingID + "/litestream.yml"
	s.CredentialPath = "/srv/state/credentials/s3/destination/v1.env"
	r := &launchReader{permitReader: permitReader{state: s}, environment: []string{"AWS_ACCESS_KEY_ID=key", "AWS_SECRET_ACCESS_KEY=secret", "AWS_SESSION_TOKEN=token+/="}}
	t.Setenv("AWS_PROFILE", "ambient")
	t.Setenv("AWS_WEB_IDENTITY_TOKEN_FILE", "/other/token")
	called := false
	req := ReplicaExecRequest{ReplicaPermitRequest: ReplicaPermitRequest{DatabaseID: s.Binding.DatabaseID, BindingID: s.Binding.BindingID, EpochID: s.Binding.EpochID, ConfigHash: s.ConfigHash}, ConfigPath: s.ConfigPath, CredentialPath: s.CredentialPath}
	err := ReplicaExec(context.Background(), r, req, func(file string, args, env []string) error {
		called = true
		if file != Executable || strings.Join(args, " ") != Executable+" replicate -config "+s.ConfigPath {
			t.Fatal("wrong executable")
		}
		if l, err := AcquireLifetimeLock(context.Background(), s.LifetimeLock); err == nil {
			l.Release()
			t.Fatal("exec not locked")
		}
		for _, entry := range env {
			if strings.Contains(entry, "ambient") || strings.Contains(entry, "WEB_IDENTITY") {
				t.Fatal("inherited provider")
			}
		}
		if !contains(env, "AWS_SESSION_TOKEN=token+/=") || !contains(env, "AWS_EC2_METADATA_DISABLED=true") {
			t.Fatal("missing SDK environment")
		}
		return errors.New("fake exec returned")
	})
	if !called || err == nil || r.reads < 2 {
		t.Fatal("missing locked permit recheck")
	}
	l, err := AcquireLifetimeLock(context.Background(), s.LifetimeLock)
	if err != nil {
		t.Fatal("failed exec leaked lock")
	}
	l.Release()
}
func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
func TestExecutionEnvironmentRejectsOtherProviders(t *testing.T) {
	for _, env := range [][]string{{}, {"AWS_ACCESS_KEY_ID=key"}, {"AWS_ACCESS_KEY_ID=key", "AWS_SECRET_ACCESS_KEY=secret", "AWS_PROFILE=other"}, {"AWS_ACCESS_KEY_ID=key", "AWS_ACCESS_KEY_ID=other", "AWS_SECRET_ACCESS_KEY=secret"}, {"AWS_ACCESS_KEY_ID=key", "AWS_SECRET_ACCESS_KEY=bad secret"}} {
		if _, err := executionEnvironment(env); err == nil {
			t.Fatal("unsafe credentials accepted")
		}
	}
}

func TestReplicaExecRechecksFenceAndEpochAfterLock(t *testing.T) {
	for _, mutate := range []func(*PermitState){func(s *PermitState) { s.Fence = FenceHeld }, func(s *PermitState) { s.Binding.EpochID = strings.Repeat("9", 32) }} {
		s := allowedState(t)
		dir := t.TempDir()
		if err := os.Chmod(dir, 0700); err != nil {
			t.Fatal(err)
		}
		s.LifetimeLock = filepath.Join(dir, "binding.lock")
		s.ConfigPath = "/srv/state/replication/" + s.Binding.BindingID + "/litestream.yml"
		s.CredentialPath = "/srv/state/credentials/s3/destination/v1.env"
		r := &launchReader{permitReader: permitReader{state: s}, onRead: func(n int, s *PermitState) {
			if n == 2 {
				mutate(s)
			}
		}}
		req := ReplicaExecRequest{ReplicaPermitRequest: ReplicaPermitRequest{DatabaseID: s.Binding.DatabaseID, BindingID: s.Binding.BindingID, EpochID: s.Binding.EpochID, ConfigHash: s.ConfigHash}, ConfigPath: s.ConfigPath, CredentialPath: s.CredentialPath}
		called := false
		if ReplicaExec(context.Background(), r, req, func(string, []string, []string) error { called = true; return nil }) == nil || called {
			t.Fatal("changed permit reached exec")
		}
		lock, err := AcquireLifetimeLock(context.Background(), s.LifetimeLock)
		if err != nil {
			t.Fatal(err)
		}
		lock.Release()
	}
}
