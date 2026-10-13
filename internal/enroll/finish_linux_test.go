package enroll

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
	"github.com/ShaulLavo/brine/internal/transport"
)

func finishClientOptions(t *testing.T) (Options, *operatorFake) {
	t.Helper()
	o, f := fakeClientOptions(t)
	if err := privateDir(o.ConfigDir); err != nil {
		t.Fatal(err)
	}
	if err := clientFile(filepath.Join(o.ConfigDir, o.Name+".identity-key"), make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	o.Finish = true
	f.facts.OwnedRunner = true
	f.facts.Snapshot.Runner.User = target.Known("brine")
	f.facts.Snapshot.Runner.Linger = target.Known(true)
	return o, f
}

func TestFinishWritesVerifiedTargetWithoutApply(t *testing.T) {
	o, f := finishClientOptions(t)
	var output bytes.Buffer
	c := Client{Runner: f, Input: strings.NewReader("fixture\n"), Output: &output, Terminal: true}
	if err := c.Run(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	actual, err := transport.LoadTarget(filepath.Join(o.ConfigDir, "fixture.json"))
	want := transport.Target{Name: "fixture", Destination: "brine@fixture", IdentityPath: strings.TrimSuffix(o.DeployKeyPath, ".pub"), PinnedHostKey: f.facts.HostKey}
	if err != nil || actual != want || f.restrictions != 1 {
		t.Fatalf("finish target=%v err=%v restriction=%d", actual, err, f.restrictions)
	}
	assertFinishActions(t, f, 2)
	if strings.Contains(output.String(), "Install missing") {
		t.Fatal("finish presented installer effects")
	}
}

func assertFinishActions(t *testing.T, f *operatorFake, want int) {
	t.Helper()
	verified := 0
	restricted := false
	for _, command := range f.commands {
		if len(command.Args) > 0 && command.Args[len(command.Args)-1] == "brine host serve" {
			restricted = true
		}
		var request HostRequest
		if len(command.Stdin) == 0 || json.Unmarshal(command.Stdin, &request) != nil {
			continue
		}
		if request.Action == "apply" {
			t.Fatal("finish replayed enrollment installer")
		}
		if request.Action != "verify" {
			continue
		}
		if request.DeployKey != f.facts.HostKey || request.VerifyOnly != (verified == 0) || (verified == 0 && restricted) {
			t.Fatalf("verification key/order/purpose changed: %+v", request)
		}
		verified++
	}
	if verified != want {
		t.Fatalf("verification calls=%d want=%d", verified, want)
	}
}

func TestFinishRefusalNeverWritesTarget(t *testing.T) {
	for _, failure := range []string{"host-verification", "missing-package", "private-key", "identity-key", "undo-conflict"} {
		t.Run(failure, func(t *testing.T) { finishRefusal(t, failure) })
	}
}

func finishRefusal(t *testing.T, failure string) {
	t.Helper()
	o, f := finishClientOptions(t)
	switch failure {
	case "host-verification":
		f.failAction = "verify"
	case "missing-package":
		delete(f.facts.Packages, "uidmap")
		f.facts.PackageInstall = []Package{{Name: "uidmap", Version: "1:4.17.4-2"}}
	case "private-key":
		f.derivedPublic = "invalid derived private key"
	case "identity-key":
		if err := os.Remove(filepath.Join(o.ConfigDir, "fixture.identity-key")); err != nil {
			t.Fatal(err)
		}
	case "undo-conflict":
		o.Undo = true
	}
	c := Client{Runner: f, Input: strings.NewReader("fixture\n"), Output: &bytes.Buffer{}, Terminal: true}
	if c.Run(context.Background(), o) == nil {
		t.Fatal("finish refusal missing")
	}
	if _, err := os.Stat(filepath.Join(o.ConfigDir, "fixture.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("finish wrote a target before verification")
	}
	for _, command := range f.commands {
		var req HostRequest
		_ = json.Unmarshal(command.Stdin, &req)
		if req.Action == "apply" || (len(command.Args) > 0 && command.Args[len(command.Args)-1] == "brine host serve") {
			t.Fatal("refused finish reached installer or restricted transport")
		}
	}
}

func TestHostVerificationRequiresOriginalIntent(t *testing.T) {
	o, _ := fakeClientOptions(t)
	key, err := os.ReadFile(o.DeployKeyPath)
	if err != nil {
		t.Fatal(err)
	}
	original, err := PublicKey(strings.TrimSpace(string(key)))
	if err != nil {
		t.Fatal(err)
	}
	for _, failure := range []string{"phase", "key", "binary"} {
		h := host{r: hostRecord{ID: "fixture", Key: original, BinaryHash: hash([]byte("original")), Files: map[string]ownedFile{binaryPath: {Hash: hash([]byte("original"))}}, Journal: Journal{Phase: Enrolled}}}
		req := HostRequest{DeployKey: original, IdentityKey: make([]byte, 32)}
		switch failure {
		case "phase":
			h.r.Journal.Phase = Applying
		case "key":
			req.DeployKey = "invalid"
		case "binary":
			h.r.Files[binaryPath] = ownedFile{Hash: hash([]byte("changed"))}
		}
		if _, err := h.verificationFiles(req); err == nil {
			t.Fatalf("host accepted changed %s intent", failure)
		}
	}
}

func TestRecordedBinaryHashRefusesChangedInstalledBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte("changed executable"), 0600); err != nil {
		t.Fatal(err)
	}
	original := hash([]byte("original executable"))
	h := host{r: hostRecord{Files: map[string]ownedFile{path: {Hash: original, Mode: 0600}}}}
	if matched, err := h.fileMatches(path, original); err == nil || matched {
		t.Fatal("changed installed bytes accepted against original binary hash")
	}
}
