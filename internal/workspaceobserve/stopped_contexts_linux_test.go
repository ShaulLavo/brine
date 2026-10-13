//go:build linux

package workspaceobserve

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestObserverAuthorizesOnlyClosedStoppedSourceAndStagingContexts(t *testing.T) {
	for _, relative := range []string{"native-restores", "apps/" + strings.Repeat("b", 32) + "/databases/" + strings.Repeat("c", 32)} {
		t.Run(relative, func(t *testing.T) { checkStoppedObservationContext(t, relative) })
	}
}

func checkStoppedObservationContext(t *testing.T, relative string) {
	t.Helper()
	base := t.TempDir()
	if err := os.Chmod(base, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, relative)
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := openDirectory(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	held, uid, err := identity(directory)
	if err != nil {
		t.Fatal(err)
	}
	scope := scope{uid: uid, roots: []string{base}}
	actual, authorizedPath, err := scope.authorize(directory)
	if err != nil || actual != held || authorizedPath != path {
		t.Fatal("actual stopped context refused scoped observation", err)
	}
	proc := procFixture(t)
	pid := strconv.Itoa(os.Getpid())
	if state := scope.scanProcess(context.Background(), proc, pid, path, held, currentPID(t)); state != "absent" {
		t.Fatal("held root-only context refused", state)
	}
	nested := filepath.Join(path, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	consumer, err := openDirectory(nested)
	if err != nil {
		t.Fatal(err)
	}
	if state := scope.scanProcess(context.Background(), proc, pid, path, held, currentPID(t)); state != "busy" {
		t.Fatal("owned descendant descriptor was self-exempted", state)
	}
	if err := consumer.Close(); err != nil {
		t.Fatal(err)
	}
	marker, err := os.OpenFile(filepath.Join(path, "marker"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if state := scope.scanProcess(context.Background(), proc, pid, path, held, currentPID(t)); state != "busy" {
		t.Fatal("owned source regular descriptor was self-exempted", state)
	}
	if err := marker.Close(); err != nil {
		t.Fatal(err)
	}

	invalid := filepath.Join(base, "unrelated")
	if err := os.Mkdir(invalid, 0700); err != nil {
		t.Fatal(err)
	}
	unrelated, err := openDirectory(invalid)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = unrelated.Close() }()
	if _, _, err := scope.authorize(unrelated); !errors.Is(err, ErrUnknown) {
		t.Fatal("arbitrary owned subtree admitted", err)
	}
}

func TestObserverClosedContextShapesRefuseArbitraryAndAliasedSubtrees(t *testing.T) {
	id := strings.Repeat("a", 32)
	for _, relative := range []string{".", "apps", "apps/" + id, "apps/" + id + "/databases", "native-restores/" + id + "/child", "apps/" + id + "/databases/" + id + "/child", "apps/" + strings.ToUpper(id) + "/databases/" + id, "apps/" + id + "/data/" + id, "native-restores/../unrelated"} {
		if validObservationRelative(relative) {
			t.Fatal("unclosed observation shape admitted", relative)
		}
	}
	s, _, _, _ := fixture(t)
	outside := t.TempDir()
	link := filepath.Join(s.roots[0], "apps")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(outside, id, "databases", id)
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	directory, err := openDirectory(source)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if _, _, err := s.authorize(directory); !errors.Is(err, ErrUnknown) {
		t.Fatal("foreign source tree supplied authority through alias", err)
	}
	held, _, err := identity(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.authorizedAlias(s.roots[0], "apps/"+id+"/databases/"+id, held); !errors.Is(err, ErrUnknown) {
		t.Fatal("canonical source path crossed symlink", err)
	}
}
