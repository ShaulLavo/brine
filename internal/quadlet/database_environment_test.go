package quadlet

import (
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/policy"
)

func TestDatabaseEnvironmentGolden(t *testing.T) {
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range []string{"one", "several"} {
		t.Run(name, func(t *testing.T) {
			d, p := persistentFixture(t)
			d.SchemaCompatibility = []data.SchemaCompatibility{{Database: "main", Accepts: []string{"v2", "v1"}, Startup: "preserve"}}
			if name == "several" {
				second := d.Databases[0]
				second.Name = "audit-log"
				second.MountPath = "/audit"
				second.Filename = "events.db"
				d.Databases = append(d.Databases, second)
				d.SchemaCompatibility = append(d.SchemaCompatibility, data.SchemaCompatibility{Database: "audit-log", Accepts: []string{"audit-v1"}, Startup: "preserve"})
				mount := p.DataMounts[0]
				mount.Database.Name = second.Name
				mount.Database.MountPath = second.MountPath
				mount.Database.Filename = second.Filename
				mount.Database.DatabaseID = data.DatabaseID(strings.Repeat("4", 32))
				mount.Database.ReplicaBindingID = data.ReplicaBindingID(strings.Repeat("5", 32))
				mount.Database.RelativeDirectory, _ = data.RelativeDirectory(mount.Database.IncarnationID, mount.Database.DatabaseID)
				mount.HostPath = path.Join(string(mount.Database.Root), mount.Database.RelativeDirectory)
				mount.ContainerPath = second.MountPath
				mount.BindingID = mount.Database.ReplicaBindingID
				p.DataMounts = append(p.DataMounts, mount)
			}
			p.DesiredHash = bind(t, d).DesiredHash
			u, err := Render(d, p, manifest())
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for _, line := range strings.Split(string(u.Bytes()), "\n") {
				if strings.HasPrefix(line, "Environment=") {
					lines = append(lines, line)
				}
			}
			got := strings.Join(lines, "\n") + "\n"
			want, err := root.ReadFile("database-" + name + ".env")
			if err != nil {
				t.Fatal(err)
			}
			if got != string(want) {
				t.Fatalf("environment mismatch\ngot %s\nwant %s", got, want)
			}
			if strings.Contains(string(u.Bytes()), "EnvironmentFile=") || strings.Contains(string(u.Bytes()), "--env-file") {
				t.Fatal("rendered environment file")
			}
			slices.Reverse(d.Databases)
			slices.Reverse(d.SchemaCompatibility)
			slices.Reverse(d.SchemaCompatibility[0].Accepts)
			v, err := Render(d, p, manifest())
			if err != nil || string(v.Bytes()) != string(u.Bytes()) {
				t.Fatal("noncanonical database environment", err)
			}
		})
	}
}

func TestDatabaseEnvironmentCannotBeOverridden(t *testing.T) {
	for _, secret := range []bool{false, true} {
		d, p := persistentFixture(t)
		if secret {
			d.Secrets = []policy.Secret{{Name: "BRINE_DB_MAIN_PATH", Reference: "token"}}
			p.Secrets = bind(t, d).Secrets
		} else {
			d.Environment = []policy.Environment{{Name: "BRINE_DB_MAIN_PATH", Value: "/other.db"}}
		}
		p.DesiredHash = bind(t, d).DesiredHash
		if _, err := Render(d, p, manifest()); err == nil {
			t.Fatal("rendered reserved override")
		}
	}
}

func TestDatabaseEnvironmentIgnoresForgedDerivedValues(t *testing.T) {
	d, p := persistentFixture(t)
	d.DatabaseEnvironment = []policy.Environment{{Name: "BRINE_DB_MAIN_PATH", Value: "/forged.db"}}
	u, err := Render(d, p, manifest())
	if err != nil {
		t.Fatal(err)
	}
	env, err := parseEnvironment(u.Bytes())
	if err != nil || env["BRINE_DB_MAIN_PATH"] != "/data/app.db" {
		t.Fatalf("derived path overridden: %+v, %v", env, err)
	}
}
