package data

import (
	"strings"
	"testing"
)

func TestIdentityAndFixedLayout(t *testing.T) {
	incarnation, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	database, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	epoch, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidID(incarnation) || !ValidID(database) || incarnation == database || database == epoch {
		t.Fatal("identities not independent 128-bit values")
	}
	relative, err := RelativeDirectory(AppIncarnationID(incarnation), DatabaseID(database))
	if err != nil || relative != "apps/"+incarnation+"/databases/"+database {
		t.Fatalf("layout: %s %v", relative, err)
	}
	prefix, err := RemotePrefix("brine", AppIncarnationID(incarnation), DatabaseID(database), ReplicaEpochID(epoch))
	if err != nil || prefix != "brine/"+relative+"/epochs/"+epoch+"/" {
		t.Fatalf("prefix: %s %v", prefix, err)
	}
	for _, bad := range []string{"", "../escape", "/absolute", "a/../b", ".."} {
		if _, err = RemotePrefix(bad, AppIncarnationID(incarnation), DatabaseID(database), ReplicaEpochID(epoch)); err == nil {
			t.Fatalf("unsafe prefix %q", bad)
		}
	}
	if _, err = RelativeDirectory("../escape", DatabaseID(database)); err == nil {
		t.Fatal("identity traversal accepted")
	}
}
func TestDatabaseDeclarationBoundary(t *testing.T) {
	valid := Database{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "../data", "App", strings.Repeat("a", 64)} {
		d := valid
		d.Name = DatabaseName(name)
		if d.Validate() == nil {
			t.Fatalf("bad name %q", name)
		}
	}
	for _, filename := range []string{".", "..", "db/file", "app.db-wal", "app.db-shm", "app.db-journal", "app.db-litestream", "a\x00b"} {
		d := valid
		d.Filename = DatabaseFilename(filename)
		if d.Validate() == nil {
			t.Fatalf("bad filename %q", filename)
		}
	}
	for _, mount := range []string{"/", "/data/../etc", "/etc", "/proc/app", "/run/secrets", "relative"} {
		d := valid
		d.MountPath = ContainerMountPath(mount)
		if d.Validate() == nil {
			t.Fatalf("bad mount %q", mount)
		}
	}
	for _, r := range []RuntimeIdentity{{}, {UID: 1}, {GID: 1}, {UID: 65536, GID: 1}, {UID: 1, GID: 65536}} {
		if r.Validate() == nil {
			t.Fatal("unsafe runtime accepted")
		}
	}
	if (RuntimeIdentity{UID: 10001, GID: 10001}).Validate() != nil {
		t.Fatal("mapped user refused")
	}
}
