package quadlet

import (
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/replication"
)

func replicaOptions(t *testing.T) ReplicaUnitOptions {
	t.Helper()
	database := strings.Repeat("1", 32)
	binding := strings.Repeat("2", 32)
	epoch := strings.Repeat("3", 32)
	incarnation := strings.Repeat("4", 32)
	b := replication.Binding{Cadence: replication.Cadence{SyncInterval: time.Minute, SnapshotInterval: 6 * time.Hour}, DatabaseID: database, BindingID: binding, EpochID: epoch, IncarnationID: incarnation, DBPath: "/srv/data/apps/" + incarnation + "/databases/" + database + "/app.db", SocketPath: "/srv/state/replication/" + binding + "/control.sock", Endpoint: "https://objects.example.invalid", Bucket: "backup-bucket", Prefix: "base/apps/" + incarnation + "/databases/" + database + "/epochs/" + epoch + "/", Region: "auto", ForcePathStyle: true}
	raw, err := replication.RenderConfig(b)
	if err != nil {
		t.Fatal(err)
	}
	return ReplicaUnitOptions{Binding: b, Config: raw, ConfigPath: "/srv/state/replication/" + binding + "/litestream.yml", CredentialPath: "/srv/state/credentials/s3/destination/v1.env", LifetimeLock: "/srv/state/replica-locks/" + binding + ".lock"}
}
func TestReplicaUnitStartsThroughPermitAndLifetimeLock(t *testing.T) {
	o := replicaOptions(t)
	u, err := RenderReplica(o)
	if err != nil {
		t.Fatal(err)
	}
	text := string(u.Bytes())
	for _, want := range []string{"UMask=0077", "EnvironmentFile=" + o.CredentialPath, "ExecStartPre=/usr/local/bin/brine host replica-permit " + o.Binding.DatabaseID + " " + o.Binding.BindingID + " " + o.Binding.EpochID + " " + replication.ConfigHash(o.Config), "ExecStart=/usr/local/bin/brine host replica-exec", "Restart=on-failure", "WantedBy=default.target"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s\n%s", want, text)
		}
	}
	for _, bad := range []string{"PartOf=", "BindsTo=", "AWS_ACCESS_KEY_ID=", "AWS_SECRET_ACCESS_KEY=", "status"} {
		if strings.Contains(text, bad) {
			t.Fatalf("unsafe unit %s", bad)
		}
	}
}
func TestReplicaUnitRejectsUnboundPaths(t *testing.T) {
	for _, mutate := range []func(*ReplicaUnitOptions){func(o *ReplicaUnitOptions) { o.ConfigPath = "/other/litestream.yml" }, func(o *ReplicaUnitOptions) { o.LifetimeLock = "/other/binding.lock" }, func(o *ReplicaUnitOptions) { o.CredentialPath = "/srv/state/credentials/s3/destination/../v1.env" }, func(o *ReplicaUnitOptions) { o.Config = []byte("dbs: []") }} {
		o := replicaOptions(t)
		mutate(&o)
		if _, err := RenderReplica(o); err == nil {
			t.Fatal("accepted unbound unit")
		}
	}
}
