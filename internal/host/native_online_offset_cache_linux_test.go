//go:build linux

package host

import (
	"os"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/store"
)

func nativeOnlineOffsetCacheFixture(t *testing.T, cacheName string) (*nativeOnlineCopy, *os.Root, string) {
	t.Helper()
	binding, staging := engineSourceFixture(t)
	root, err := openEngineSource(binding)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := root.MkdirAll("replicas/7/clog", 0700); err != nil {
		t.Fatal(err)
	}
	if err := root.WriteFile("replicas/7/clog/00000000000000000000.stdb.log", []byte("captured native log"), 0600); err != nil {
		t.Fatal(err)
	}
	cachePath := "replicas/7/clog/" + cacheName
	if err := root.WriteFile(cachePath, make([]byte, 4<<20), 0600); err != nil {
		t.Fatal(err)
	}
	identity, err := nativeRestoreDirectoryIdentity(root, ".")
	if err != nil {
		t.Fatal(err)
	}
	parent, err := engineOpenRoot(staging)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = parent.Close() })
	permit := store.EnginePermit{Reserved: store.ReservedEngine{Database: binding}, Allocation: &data.AllocationReceipt{DirectoryDevice: identity.Device, DirectoryInode: identity.Inode}}
	copied, err := captureNativeOnlineCopy(t.Context(), permit, parent, staging)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = copied.close() })
	return copied, root, cachePath
}

func nativeRewriteOffsetFixture(t *testing.T, root *os.Root, name string) {
	t.Helper()
	file, err := root.OpenFile(name, os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteAt([]byte{1}, 0)
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(err, closeErr)
	}
}

func TestNativeOnlineCopyAllowsQualifiedOffsetIndexUpdate(t *testing.T) {
	copied, root, cache := nativeOnlineOffsetCacheFixture(t, "00000000000000000000.stdb.ofs")
	nativeRewriteOffsetFixture(t, root, cache)
	if err := copied.unchanged(t.Context()); err != nil {
		t.Fatal("optional seek index update invalidated captured native data", err)
	}
	if _, err := copied.root.Lstat(cache); !os.IsNotExist(err) {
		t.Fatal("mutable offset index entered the captured source", err)
	}
	nativeRewriteOffsetFixture(t, root, "replicas/7/clog/00000000000000000000.stdb.log")
	if copied.unchanged(t.Context()) == nil {
		t.Fatal("captured native log rewrite was admitted")
	}
}

func TestNativeOnlineCopyKeepsUnknownOffsetEntriesSealed(t *testing.T) {
	for _, name := range []string{"00000000000000000001.stdb.ofs", "0000000000000000000.stdb.ofs", "orphan.ofs"} {
		t.Run(name, func(t *testing.T) {
			copied, root, cache := nativeOnlineOffsetCacheFixture(t, name)
			nativeRewriteOffsetFixture(t, root, cache)
			if copied.unchanged(t.Context()) == nil {
				t.Fatal("unqualified offset entry update was ignored")
			}
		})
	}
}

func TestNativeOnlineCopyRejectsOffsetCacheMetadataDrift(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*os.Root, string) error
	}{
		{"size", func(root *os.Root, cache string) error { return root.WriteFile(cache, []byte{1}, 0600) }},
		{"replacement", func(root *os.Root, cache string) error {
			if err := root.Remove(cache); err != nil {
				return err
			}
			return root.WriteFile(cache, make([]byte, 4<<20), 0600)
		}},
		{"alias", func(root *os.Root, cache string) error { return root.Link(cache, "control-db/cache-alias") }},
		{"log_sibling", func(root *os.Root, _ string) error {
			return root.Remove("replicas/7/clog/00000000000000000000.stdb.log")
		}},
	}
	for _, check := range cases {
		t.Run(check.name, func(t *testing.T) { testNativeOnlineCacheDrift(t, check.mutate) })
	}
}

func testNativeOnlineCacheDrift(t *testing.T, mutate func(*os.Root, string) error) {
	t.Helper()
	copied, root, cache := nativeOnlineOffsetCacheFixture(t, "00000000000000000000.stdb.ofs")
	if err := mutate(root, cache); err != nil {
		t.Fatal(err)
	}
	if copied.unchanged(t.Context()) == nil {
		t.Fatal("offset cache metadata drift was admitted")
	}
}

func TestNativeOnlineOffsetCacheRequiresSafeSiblingAndIdentity(t *testing.T) {
	copied, _, cache := nativeOnlineOffsetCacheFixture(t, "00000000000000000000.stdb.ofs")
	var member engineTreeMember
	for _, candidate := range copied.before.members {
		if candidate.Name == cache {
			member = candidate
		}
	}
	if !nativeOnlineOffsetCacheMember(member, copied.before) {
		t.Fatal("qualified cache not recognized")
	}
	unsafe := []engineTreeMember{member, member, member, member, member, member}
	unsafe[0].Device++
	unsafe[1].MountID++
	unsafe[2].Links++
	unsafe[3].Size = (32 << 20) + 1
	unsafe[4].Mode = 0644
	unsafe[5].UID++
	for _, changed := range unsafe {
		if nativeOnlineOffsetCacheMember(changed, copied.before) {
			t.Fatal("unsafe cache was excluded from capture")
		}
	}
	for i, sibling := range copied.before.members {
		if sibling.Name == "replicas/7/clog/00000000000000000000.stdb.log" {
			copied.before.members[i].MountID++
		}
	}
	if nativeOnlineOffsetCacheMember(member, copied.before) {
		t.Fatal("foreign log sibling qualified the cache")
	}
}
