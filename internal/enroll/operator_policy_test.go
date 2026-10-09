//go:build linux

package enroll

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

type fakeOperatorFile struct {
	data                       []byte
	installed                  string
	installs, removes, retains int
	readErr                    error
}

func (f *fakeOperatorFile) Read() ([]byte, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	if f.data == nil {
		return nil, os.ErrNotExist
	}
	return f.data, nil
}
func (f *fakeOperatorFile) Install(context.Context) error {
	f.installs++
	f.data = []byte(defaultOperatorPolicy)
	f.installed = hash(f.data)
	return nil
}
func (f *fakeOperatorFile) InstalledHash() (string, bool) { return f.installed, f.installed != "" }
func (f *fakeOperatorFile) Remove() error                 { f.removes++; f.data = nil; return nil }
func (f *fakeOperatorFile) Retain() error                 { f.retains++; return nil }

func TestOperatorPolicyEditRerunAndUndo(t *testing.T) {
	ctx := context.Background()
	f := &fakeOperatorFile{}
	step := operatorPolicyStep(f)
	s := &memoryStore{}
	j := Journal{}
	if err := Apply(ctx, s, &j, []Step{step}); err != nil {
		t.Fatal(err)
	}
	f.data = []byte("operator-edited policy")
	if err := Apply(ctx, s, &j, []Step{step}); err != nil || j.Phase != Enrolled || f.installs != 1 {
		t.Fatalf("rerun: phase=%s installs=%d err=%v", j.Phase, f.installs, err)
	}
	if err := Undo(ctx, s, &j, []Step{step}); err != nil || j.Phase != Removed || f.removes != 0 || f.retains != 1 || string(f.data) != "operator-edited policy" {
		t.Fatalf("undo: phase=%s removed=%d retained=%d err=%v", j.Phase, f.removes, f.retains, err)
	}
}
func TestOperatorPolicyPreexistingAndUnedited(t *testing.T) {
	for _, preexisting := range []bool{false, true} {
		f := &fakeOperatorFile{}
		if preexisting {
			f.data = []byte(defaultOperatorPolicy)
		}
		s := &memoryStore{}
		j := Journal{}
		steps := []Step{operatorPolicyStep(f)}
		if err := Apply(context.Background(), s, &j, steps); err != nil {
			t.Fatal(err)
		}
		if err := Undo(context.Background(), s, &j, steps); err != nil {
			t.Fatal(err)
		}
		if preexisting && (f.installs != 0 || f.removes != 0 || f.retains != 1) || !preexisting && (f.installs != 1 || f.removes != 1 || f.retains != 0) {
			t.Fatalf("preexisting=%v fixture=%+v", preexisting, f)
		}
	}
}

func TestOperatorPolicyTrustFailureDoesNotMutate(t *testing.T) {
	f := &fakeOperatorFile{data: []byte("changed"), readErr: os.ErrPermission}
	step := operatorPolicyStep(f)
	if _, err := step.Check(context.Background()); err == nil {
		t.Fatal("untrusted policy accepted")
	}
	if err := step.Apply(context.Background()); err == nil {
		t.Fatal("untrusted policy overwritten")
	}
	if err := step.Undo(context.Background()); err == nil {
		t.Fatal("untrusted policy removed or retained as trusted")
	}
	if f.installs+f.removes+f.retains != 0 {
		t.Fatal("mutated untrusted policy")
	}
}

func TestUndoPreflightAcceptsEditedTrustedPolicyOnly(t *testing.T) {
	files := map[string]ownedFile{operatorPolicyPath: {Hash: hash([]byte(defaultOperatorPolicy))}, "fixture-key": {Hash: hash([]byte("key"))}}
	read := func(string) ([]byte, error) { return []byte("key"), nil }
	if err := verifyUndoFiles(files, read, func() ([]byte, error) { return []byte("edited"), nil }); err != nil {
		t.Fatal(err)
	}
	if err := verifyUndoFiles(files, read, func() ([]byte, error) { return nil, os.ErrPermission }); err == nil {
		t.Fatal("untrusted edited policy accepted")
	}
	if err := verifyUndoFiles(files, func(string) ([]byte, error) { return []byte("changed key"), nil }, func() ([]byte, error) { return []byte("edited"), nil }); err == nil {
		t.Fatal("ordinary file drift accepted")
	}
}

func TestOperatorPolicyRequiresProtectedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.toml")
	if err := os.WriteFile(path, []byte("operator contents"), 0644); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorPolicyMetadata(keyOwnershipInfo{info, 0}); err != nil {
		t.Fatal(err)
	}
	if err := operatorPolicyMetadata(keyOwnershipInfo{info, 1234}); err == nil {
		t.Fatal("runner-owned policy accepted")
	}
	if err := os.Chmod(path, 0664); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorPolicyMetadata(keyOwnershipInfo{info, 0}); err == nil {
		t.Fatal("writable policy accepted")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(path), "linked-policy")
	if err := os.Link(path, link); err != nil {
		t.Fatal(err)
	}
	info, err = os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorPolicyMetadata(keyOwnershipInfo{info, 0}); err == nil {
		t.Fatal("hard-linked policy accepted")
	}
	info, err = os.Lstat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if err := operatorPolicyMetadata(keyOwnershipInfo{info, 0}); err == nil {
		t.Fatal("directory accepted as policy")
	}
}
