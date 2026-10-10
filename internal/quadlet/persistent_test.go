package quadlet

import (
	"path"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

func persistentFixture(t testing.TB) (policy.Desired, plan.Plan) {
	t.Helper()
	d, _ := fixture(t)
	d.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	d.Databases = []data.Database{{Name: "main", PersistentRoot: "/srv/data", MountPath: "/data", Filename: "app.db", BackupDestination: "primary"}}
	p := bind(t, d)
	p.Runtime = &data.RuntimeIdentity{UID: 10001, GID: 10001}
	incarnation := data.AppIncarnationID(strings.Repeat("1", 32))
	database := data.DatabaseID(strings.Repeat("2", 32))
	binding := data.ReplicaBindingID(strings.Repeat("3", 32))
	relative, err := data.RelativeDirectory(incarnation, database)
	if err != nil {
		t.Fatal(err)
	}
	p.DataMounts = []data.Mount{{Database: data.DatabaseBinding{DatabaseID: database, Name: "main", IncarnationID: incarnation, Root: "/srv/data", RelativeDirectory: relative, MountPath: "/data", Filename: "app.db", ReplicaBindingID: binding}, HostPath: path.Join("/srv/data", relative), ContainerPath: "/data", BindingID: binding}}
	return d, p
}

func TestPersistentRenderIdentityMountAndIndependentReplicaOrdering(t *testing.T) {
	d, p := persistentFixture(t)
	u, err := Render(d, p, manifest())
	if err != nil {
		t.Fatal(err)
	}
	text := string(u.Bytes())
	service := "brine-litestream-" + string(p.DataMounts[0].BindingID) + ".service"
	for _, want := range []string{
		"Wants=" + service + "\n", "After=" + service + "\n",
		"UserNS=keep-id:uid=10001,gid=10001\n", "User=10001\n", "Group=10001\n",
		"--umask=0077", "UMask=0077\n",
		"Volume=" + p.DataMounts[0].HostPath + ":/data:rw\n",
		"ExecStartPre=/usr/local/bin/brine host writer-permit " + string(p.DataMounts[0].Database.IncarnationID) + "\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
	permit := "ExecStartPre=/usr/local/bin/brine host writer-permit " + string(p.DataMounts[0].Database.IncarnationID) + "\n"
	attempt := "ExecStartPre=/usr/local/bin/brine host writer-attempt " + string(p.DataMounts[0].Database.IncarnationID) + "\n"
	if !strings.Contains(text, permit+attempt) {
		t.Fatal("absence evidence not consumed after read-only permit", text)
	}
	for _, forbidden := range []string{"PartOf=", "BindsTo=", ":ro", "EnvironmentFile="} {
		if strings.Contains(text, forbidden) {
			t.Fatal("coupled replica or leaked credentials", forbidden)
		}
	}
}

func TestPersistentVolumePreservesLiteralPathsAndEscapesExpansions(t *testing.T) {
	for _, root := range []string{"/srv/data with spaces", `/srv/data"quotes'\\backslash`, "/srv/data-%n-$HOME"} {
		t.Run(root, func(t *testing.T) {
			d, p := persistentFixture(t)
			d.Databases[0].PersistentRoot = data.PersistentRoot(root)
			p.DesiredHash = bind(t, d).DesiredHash
			p.DataMounts[0].Database.Root = data.PersistentRoot(root)
			p.DataMounts[0].HostPath = path.Join(root, p.DataMounts[0].Database.RelativeDirectory)
			u, err := Render(d, p, manifest())
			if err != nil {
				t.Fatal(err)
			}
			var volume string
			for _, line := range strings.Split(string(u.Bytes()), "\n") {
				if strings.HasPrefix(line, "Volume=") {
					volume = strings.TrimPrefix(line, "Volume=")
				}
			}
			// LookupAll keeps quotes and backslashes; generated ExecStart expands %% and $$ once.
			volume = strings.ReplaceAll(strings.ReplaceAll(volume, "%%", "%"), "$$", "$")
			want := p.DataMounts[0].HostPath + ":/data:rw"
			if volume != want {
				t.Fatalf("literal volume = %q, want %q", volume, want)
			}
		})
	}
}

func TestPersistentVolumeRefusesColonDelimitedPaths(t *testing.T) {
	d, p := persistentFixture(t)
	d.Databases[0].PersistentRoot = "/srv/data:other"
	p.DesiredHash = bind(t, d).DesiredHash
	p.DataMounts[0].Database.Root = d.Databases[0].PersistentRoot
	p.DataMounts[0].HostPath = path.Join(string(d.Databases[0].PersistentRoot), p.DataMounts[0].Database.RelativeDirectory)
	if _, err := Render(d, p, manifest()); err == nil {
		t.Fatal("rendered ambiguous colon-delimited volume")
	}
}

func TestPersistentRenderRefusesUnboundOrChangedData(t *testing.T) {
	cases := map[string]func(*policy.Desired, *plan.Plan){
		"missing-runtime": func(_ *policy.Desired, p *plan.Plan) { p.Runtime = nil },
		"runtime-drift":   func(_ *policy.Desired, p *plan.Plan) { p.Runtime.UID++ },
		"missing-mount":   func(_ *policy.Desired, p *plan.Plan) { p.DataMounts = nil },
		"foreign-root":    func(_ *policy.Desired, p *plan.Plan) { p.DataMounts[0].Database.Root = "/other" },
		"foreign-source":  func(_ *policy.Desired, p *plan.Plan) { p.DataMounts[0].HostPath = "/other/data" },
		"foreign-mount":   func(_ *policy.Desired, p *plan.Plan) { p.DataMounts[0].ContainerPath = "/other" },
		"binding-drift": func(_ *policy.Desired, p *plan.Plan) {
			p.DataMounts[0].BindingID = data.ReplicaBindingID(strings.Repeat("4", 32))
		},
		"filename-drift": func(_ *policy.Desired, p *plan.Plan) { p.DataMounts[0].Database.Filename = "other.db" },
		"relative-drift": func(_ *policy.Desired, p *plan.Plan) { p.DataMounts[0].Database.RelativeDirectory = "other" },
		"invalid-id":     func(_ *policy.Desired, p *plan.Plan) { p.DataMounts[0].Database.IncarnationID = "unknown" },
		"extra-mount":    func(_ *policy.Desired, p *plan.Plan) { p.DataMounts = append(p.DataMounts, p.DataMounts[0]) },
		"stateless-mount": func(d *policy.Desired, p *plan.Plan) {
			d.Databases = nil
			d.Runtime = nil
			p.DesiredHash = bind(t, *d).DesiredHash
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			d, p := persistentFixture(t)
			mutate(&d, &p)
			if _, err := Render(d, p, manifest()); err == nil {
				t.Fatal("rendered inconsistent persistent plan")
			}
		})
	}
}
