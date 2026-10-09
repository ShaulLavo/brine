//go:build linux && pi_integration

package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/caddy"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/quadlet"
	"github.com/ShaulLavo/brine/internal/spec"
	"github.com/ShaulLavo/brine/internal/systemd"
	"github.com/ShaulLavo/brine/internal/target"
)

const imagePin = "docker.io/nginxinc/nginx-unprivileged@sha256:7377697a821c131a924a7105fafbe7414db4e9fcc77a6f08f776f33f141ec3f8"
const manifestPin = "sha256:e00b7e2763a0dfec9ec6d99253612510c253df47d7218cdd35c4e465b4e9ad1f"
const fixtureName = "fixture"
const secretName = "brine-fixture-token-v1"
const fixtureHost = "fixture.localhost"
const caddyRoot = "/etc/caddy/brine"

var stage = flag.String("fixture-stage", "", "explicitly authorized runner fixture stage: prepare, drills, probe, cleanup")

type receipt struct {
	Plan     plan.Plan
	UnitHash string
	MainHash string
	Caddy    caddy.State
	Boot     string
}

type fixture struct {
	t            *testing.T
	ctx          context.Context
	home, record string
	session      localexec.Session
	pod          *podman.Client
	sd           *systemd.Client
	image        podman.Image
	secret       podman.Name
	service      systemd.Unit
	app          spec.App
	desired      policy.Desired
	policy       policy.Policy
}

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}
func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
func hash(b []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(b)) }

func inputs(t *testing.T) (spec.App, policy.Policy, policy.Desired) {
	t.Helper()
	app := must(spec.Parse([]byte(`schema_version=1
name="fixture"
image="` + imagePin + `"
container_port=8080
domains=["` + fixtureHost + `"]
[environment]
FIXTURE_ESCAPED="quote\" slash\\ %n $HOME café"
[secrets]
FIXTURE_TOKEN="token"
`)))
	p := must(policy.Parse([]byte(`schema_version=1
version="p02-fixture-1"
allowed_domains=["` + fixtureHost + `"]
persistent_roots=[]
[[allowed_registries]]
host="docker.io"
repository_prefixes=["nginxinc"]
[app_ports]
min=20080
max=20080
[resources]
memory_mb=128
pids_limit=64
[allowed_secrets]
fixture=["token"]
`)))
	return app, p, must(policy.Normalize(app, p))
}

func TestFixtureInputs(t *testing.T) {
	_, _, d := inputs(t)
	if string(d.Image) != imagePin || d.AppPorts.Min != 20080 {
		t.Fatal("fixture bounds changed")
	}
}

func TestPiFixture(t *testing.T) {
	if *stage == "" {
		t.Skip("requires named disposable host authorization and -fixture-stage")
	}
	if *stage != "prepare" && *stage != "probe" && *stage != "cleanup" && *stage != "drills" {
		t.Fatal("unknown fixture stage")
	}
	identity := must(user.Current())
	if identity.Username != "brine" || os.Getuid() == 0 {
		t.Fatal("run as the enrolled unprivileged runner")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	s := must(localexec.NewSession(localexec.ExecRunner{}, uint32(os.Getuid()), identity.HomeDir, 90*time.Second))
	a, p, d := inputs(t)
	f := fixture{t: t, ctx: ctx, home: identity.HomeDir, record: filepath.Join(identity.HomeDir, ".local/share/brine/p02-fixture.json"), session: s, pod: podman.New(s), sd: systemd.New(s), image: must(podman.ParseImage(imagePin)), secret: must(podman.ParseName(secretName)), service: must(systemd.ParseUnit("fixture.service")), app: a, policy: p, desired: d}
	switch *stage {
	case "prepare":
		f.prepare()
	case "drills":
		r := f.load()
		f.caddyFailureChecks(&r, must(os.ReadFile("/etc/caddy/Caddyfile")))
	case "probe":
		f.probe()
	case "cleanup":
		f.cleanup()
	}
}

func (f fixture) run(path string, args ...string) string {
	f.t.Helper()
	r, e := f.session.Execute(f.ctx, path, args, nil, false)
	check(f.t, e)
	return strings.TrimSpace(r.Stdout)
}
func (f fixture) mutate(path string, args ...string) {
	f.t.Helper()
	_, e := f.session.Execute(f.ctx, path, args, nil, true)
	check(f.t, e)
}
func (f fixture) collect() target.Snapshot {
	f.t.Helper()
	return must((inventory.Collector{FS: inventory.HostFS{}, Runner: localexec.ExecRunner{}, IdentityKey: []byte("p02-disposable-fixture-identity"), StateGeneration: func(context.Context) (uint64, error) { return 0, nil }}).Collect(f.ctx))
}
func (f fixture) save(r receipt) {
	f.t.Helper()
	b := must(json.Marshal(r))
	check(f.t, os.MkdirAll(filepath.Dir(f.record), 0700))
	temp := f.record + ".tmp"
	file := must(os.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600))
	_, e := file.Write(b)
	check(f.t, e)
	check(f.t, file.Sync())
	check(f.t, file.Close())
	check(f.t, os.Rename(temp, f.record))
	dir := must(os.Open(filepath.Dir(f.record)))
	check(f.t, dir.Sync())
	check(f.t, dir.Close())
}
func (f fixture) load() receipt {
	f.t.Helper()
	b := must(os.ReadFile(f.record))
	var r receipt
	check(f.t, json.Unmarshal(b, &r))
	if r.MainHash != hash(must(os.ReadFile("/etc/caddy/Caddyfile"))) || r.Plan.App != fixtureName {
		f.t.Fatal("fixture receipt drift")
	}
	return r
}

func (f fixture) prepare() {
	if _, e := os.Lstat(f.record); !os.IsNotExist(e) {
		f.t.Fatal("fixture receipt exists; inspect before retry")
	}
	if must(f.pod.ImageExists(f.ctx, f.image)) || must(f.pod.SecretExists(f.ctx, f.secret)) {
		f.t.Fatal("fixture image or secret already exists")
	}
	if _, e := os.Lstat(filepath.Join(f.home, quadlet.ActiveDirectory, "fixture.container")); !os.IsNotExist(e) {
		f.t.Fatal("fixture unit already exists")
	}
	cm := must(caddy.NewManager(caddyRoot, caddyValidator{f.session}, reloader{f.sd}))
	defer cm.Close()
	baseline := must(cm.Observe())
	if len(baseline.Files) != 0 {
		f.t.Fatal("requires empty enrolled Caddy tree")
	}
	entries := must(os.ReadDir(caddyRoot))
	if len(entries) != 2 {
		f.t.Fatal("preexisting Caddy artifacts require operator inspection")
	}
	for _, entry := range entries {
		if entry.Name() != "current" && entry.Name() != fmt.Sprintf("gen-%d", baseline.Generation) {
			f.t.Fatal("unexpected Caddy baseline artifact")
		}
	}
	main := must(os.ReadFile("/etc/caddy/Caddyfile"))
	r := receipt{MainHash: hash(main), Boot: strings.TrimSpace(string(must(os.ReadFile("/proc/sys/kernel/random/boot_id")))), Caddy: baseline}
	f.save(r)
	check(f.t, f.pod.Pull(f.ctx, f.image))
	info := must(f.pod.Inspect(f.ctx, f.image))
	if info.IndexDigest != strings.Split(imagePin, "@")[1] || info.ManifestDigest != manifestPin || info.Platform.OS != "linux" || info.Platform.Architecture != "arm64" {
		f.t.Fatal("image index/platform binding mismatch")
	}
	f.t.Log("verified immutable multi-platform index and arm64 manifest")
	check(f.t, f.pod.CreateSecret(f.ctx, f.secret, []byte("public-fixture-token")))
	snap := f.collect()
	// This adapter fixture is not connected apply. Preserve and explicitly test the
	// real catch-all refusal, then plan only the separately authorized test route.
	liveRoutes := snap.LiveCaddyFiles
	original := must(plan.Build(plan.Input{Desired: f.desired, Snapshot: snap, Image: plan.Image{Digest: info.IndexDigest, Platform: target.Platform{OS: info.Platform.OS, Arch: info.Platform.Architecture}, ManifestDigest: target.Known(info.ManifestDigest)}, State: plan.BrineState{Target: snap.Identity, Generation: 0, Releases: []plan.CurrentRelease{}}}))
	if len(original.Conflicts) != 1 || original.Conflicts[0].Code != plan.DomainOwned {
		f.t.Fatal("expected protected package-default catch-all refusal")
	}
	f.t.Log("real planner refuses the unowned catch-all; fixture-only route scope does not authorize connected apply")
	snap.LiveCaddyFiles = target.Known([]target.LiveCaddyFile{})
	p := must(plan.Build(plan.Input{Desired: f.desired, Snapshot: snap, Image: plan.Image{Digest: info.IndexDigest, Platform: target.Platform{OS: info.Platform.OS, Arch: info.Platform.Architecture}, ManifestDigest: target.Known(info.ManifestDigest)}, State: plan.BrineState{Target: snap.Identity, Generation: 0, Releases: []plan.CurrentRelease{}}}))
	if liveRoutes.Value == nil {
		f.t.Fatal("live root routes unknown")
	}
	if p.Kind != plan.Create || len(p.Conflicts) != 0 {
		f.t.Fatalf("fixture plan refused: %v", p.Conflicts)
	}
	u := must(quadlet.Render(f.desired, p, *p.Image.ManifestDigest.Value))
	r.Plan = p
	r.UnitHash = u.Hash()
	f.save(r)
	qm := must(quadlet.NewManager(f.home, quadletValidator{}))
	defer qm.Close()
	check(f.t, qm.Install(f.ctx, u, ""))
	check(f.t, f.sd.DaemonReload(f.ctx))
	check(f.t, f.sd.Start(f.ctx, f.service))
	site := must(caddy.NewSite(f.app, f.policy, spec.Port(p.HostPort)))
	result := must(cm.Apply(f.ctx, main, baseline, caddy.Put(site)))
	if result.Outcome != caddy.Applied {
		f.t.Fatal(result.Outcome)
	}
	r.Caddy = result.Next
	f.save(r)
	f.health(p.HostPort)
	f.runtimeChecks()
	f.caddyFailureChecks(&r, main)
	f.t.Log("prepare complete; fixture persists, reboot controlled by operator")
}

func (f fixture) health(port target.Port) {
	f.t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	direct := fmt.Sprintf("http://127.0.0.1:%d/", port)
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == fixtureHost+":443" {
			address = "127.0.0.1:443"
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
	for time.Now().Before(deadline) {
		ok := true
		for _, url := range []string{direct, "https://" + fixtureHost + "/"} {
			request := must(http.NewRequestWithContext(f.ctx, http.MethodGet, url, nil))
			res, e := client.Do(request)
			if e != nil {
				ok = false
				continue
			}
			b, e := io.ReadAll(io.LimitReader(res.Body, 65536))
			res.Body.Close()
			if e != nil || res.StatusCode != 200 || !strings.Contains(string(b), "Welcome to nginx!") {
				ok = false
			}
		}
		if ok {
			f.t.Log("direct and routed HTTP 200 with expected fixture body")
			return
		}
		time.Sleep(time.Second)
	}
	f.t.Fatal("fixture health deadline exceeded")
}

func (f fixture) runtimeChecks() {
	if !must(f.sd.IsActive(f.ctx, f.service)) {
		f.t.Fatal("fixture unit inactive")
	}
	if f.run("podman", "exec", "systemd-fixture", "printenv", "FIXTURE_TOKEN") != "public-fixture-token" {
		f.t.Fatal("secret not delivered")
	}
	if f.run("podman", "exec", "systemd-fixture", "printenv", "FIXTURE_ESCAPED") != "quote\" slash\\ %n $HOME café" {
		f.t.Fatal("environment did not round trip")
	}
	if !strings.Contains(f.run("curl", "--disable", "--noproxy", "*", "--silent", "--fail", "--max-time", "2", "http://127.0.0.1:2019/config/"), "apps") {
		f.t.Fatal("host admin unavailable; denial would be vacuous")
	}
	for _, host := range []string{"127.0.0.1", "host.containers.internal", "host.docker.internal"} {
		r, e := f.session.Execute(f.ctx, "podman", []string{"exec", "systemd-fixture", "wget", "-T", "3", "-O", "/dev/null", "http://" + host + ":2019/config/"}, nil, false)
		if e == nil || r.ExitCode != 1 {
			f.t.Fatal("container admin denial not observed")
		}
	}
	f.t.Log("secret and escaped environment delivered; admin denied via loopback and both aliases")
}

func (f fixture) probe() {
	r := f.load()
	boot := strings.TrimSpace(string(must(os.ReadFile("/proc/sys/kernel/random/boot_id"))))
	if boot == r.Boot {
		f.t.Fatal("full reboot not observed")
	}
	f.health(r.Plan.HostPort)
	f.runtimeChecks()
	props := must(f.sd.Show(f.ctx, f.service))
	f.t.Logf("unit active at %.3f seconds after boot", float64(props.ActiveEnterTimestampMonotonic)/1e6)
	s := f.collect()
	if s.Apps.Value == nil || len(*s.Apps.Value) != 1 || (*s.Apps.Value)[0].Name != fixtureName {
		f.t.Fatal("inventory lost fixture app")
	}
	app := (*s.Apps.Value)[0]
	if app.Secrets.Value == nil || len(*app.Secrets.Value) != 1 || (*app.Secrets.Value)[0].Name != secretName {
		f.t.Fatal("inventory lost fixture secret")
	}
	if app.AllocatedHostPort.Value == nil || *app.AllocatedHostPort.Value != r.Plan.HostPort {
		f.t.Fatal("inventory lost running fixture port")
	}
	if s.CaddyConfig.Value == nil || s.CaddyConfig.Value.Generation != r.Caddy.Generation || len(s.CaddyConfig.Value.Files) != 1 {
		f.t.Fatal("inventory lost Caddy generation")
	}
	f.t.Log("post-reboot inventory binds app, live port, unit hash, generation and secret name/ID")
}

func (f fixture) cleanup() {
	r := f.load()
	cm := must(caddy.NewManager(caddyRoot, caddyValidator{f.session}, reloader{f.sd}))
	defer cm.Close()
	site := must(caddy.NewSite(f.app, f.policy, spec.Port(r.Plan.HostPort)))
	r.Caddy.Sites = map[string]caddy.Site{"fixture.caddy": site}
	check(f.t, cm.CheckDrift(r.Caddy))
	result := must(cm.Apply(f.ctx, must(os.ReadFile("/etc/caddy/Caddyfile")), r.Caddy, caddy.Remove(spec.Name(fixtureName))))
	if result.Outcome != caddy.Applied {
		f.t.Fatal(result.Outcome)
	}
	check(f.t, f.sd.Stop(f.ctx, f.service))
	qm := must(quadlet.NewManager(f.home, quadletValidator{}))
	defer qm.Close()
	check(f.t, qm.Rollback(f.ctx, "fixture.container", r.UnitHash, ""))
	check(f.t, f.sd.DaemonReload(f.ctx))
	f.mutate("podman", "secret", "rm", secretName)
	f.mutate("podman", "rmi", imagePin)
	prior := filepath.Join(caddyRoot, fmt.Sprintf("gen-%d", r.Caddy.Generation), "fixture.caddy")
	if hash(must(os.ReadFile(prior))) != r.Caddy.Files["fixture.caddy"] {
		f.t.Fatal("cleanup generation drift")
	}
	check(f.t, os.Remove(prior))
	check(f.t, os.Remove(filepath.Dir(prior)))
	for _, generation := range []uint64{r.Caddy.Generation, result.Next.Generation} {
		path := filepath.Join(caddyRoot, fmt.Sprintf("candidate-gen-%d.caddy", generation))
		main := must(os.ReadFile("/etc/caddy/Caddyfile"))
		expected := strings.Replace(string(main), "import "+caddyRoot+"/current/*.caddy", "import "+caddyRoot+fmt.Sprintf("/gen-%d/*.caddy", generation), 1)
		if string(must(os.ReadFile(path))) != expected {
			f.t.Fatal("cleanup candidate drift")
		}
		check(f.t, os.Remove(path))
	}

	s := f.collect()
	if s.Apps.Value == nil || len(*s.Apps.Value) != 0 {
		f.t.Fatal("cleanup left app inventory")
	}
	if must(f.pod.ImageExists(f.ctx, f.image)) || must(f.pod.SecretExists(f.ctx, f.secret)) {
		f.t.Fatal("cleanup left image or secret")
	}
	if f.run("podman", "ps", "-a", "--format", "{{.Names}}") != "" {
		f.t.Fatal("cleanup left container")
	}
	if s.CaddyConfig.Value == nil || len(s.CaddyConfig.Value.Files) != 0 {
		f.t.Fatal("cleanup left Caddy app entry")
	}

	check(f.t, os.Remove(f.record))
	f.t.Log("fixture resources removed; enrollment and empty Caddy generation retained")
}

type caddyValidator struct{ session localexec.Session }

func (v caddyValidator) Validate(ctx context.Context, path string) error {
	_, e := v.session.Execute(ctx, "caddy", []string{"validate", "--adapter", "caddyfile", "--config", path}, nil, false)
	return e
}
func (v caddyValidator) Adapt(ctx context.Context, path string) ([]byte, error) {
	r, e := v.session.Execute(ctx, "caddy", []string{"adapt", "--adapter", "caddyfile", "--config", path}, nil, false)
	return []byte(r.Stdout), e
}

type reloader struct{ sd *systemd.Client }

func (r reloader) Reload(ctx context.Context) error { return r.sd.ReloadCaddy(ctx) }

type quadletValidator struct{}

func (quadletValidator) Validate(ctx context.Context, c quadlet.Candidate) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	identity, err := user.Current()
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "/usr/lib/systemd/system-generators/podman-system-generator", "--user", "--dryrun")
	cmd.Dir = c.Directory
	cmd.Env = []string{"HOME=" + identity.HomeDir, "USER=" + identity.Username, "PATH=/usr/bin:/bin", "LC_ALL=C", "XDG_RUNTIME_DIR=/run/user/" + identity.Uid, "QUADLET_UNIT_DIRS=" + c.Directory}
	output := &limitedBuffer{}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = 100 * time.Millisecond
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("isolated Quadlet generator failed")
	}
	if output.exceeded || !strings.Contains(output.String(), "---"+strings.TrimSuffix(c.UnitName, ".container")+".service---") {
		return fmt.Errorf("isolated Quadlet generator did not emit expected service")
	}
	return nil
}

type limitedBuffer struct {
	bytes.Buffer
	exceeded bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := 256*1024 - b.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.exceeded = true
	}
	_, err := b.Buffer.Write(p)
	return n, err
}

func (f fixture) caddyFailureChecks(r *receipt, main []byte) {
	cm := must(caddy.NewManager(caddyRoot, caddyValidator{f.session}, reloader{f.sd}))
	defer cm.Close()
	site := must(caddy.NewSite(f.app, f.policy, spec.Port(r.Plan.HostPort)))
	r.Caddy.Sites = map[string]caddy.Site{"fixture.caddy": site}
	result, e := cm.Apply(f.ctx, append([]byte("invalid_fixture_directive\n"), main...), r.Caddy, caddy.Put(site))
	if e == nil || result.Stage != "validate" || result.Outcome != caddy.Unchanged {
		f.t.Fatal("invalid candidate not refused before activation")
	}
	check(f.t, cm.CheckDrift(r.Caddy))
	f.health(r.Plan.HostPort)
	f.t.Log("invalid candidate rejected; prior disk and live route unchanged")
	failure := &reloadFault{sd: f.sd}
	fm := must(caddy.NewManager(caddyRoot, caddyValidator{f.session}, failure))
	defer fm.Close()
	result, e = fm.Apply(f.ctx, main, r.Caddy, caddy.Put(site))
	if e == nil || result.Outcome != caddy.RolledBack || failure.calls != 2 {
		f.t.Fatal("real failed reload did not restore prior generation")
	}
	check(f.t, cm.CheckDrift(r.Caddy))
	f.health(r.Plan.HostPort)
	f.t.Log("real systemd reload rejected invalid activated fixture config; adapter rolled back and reloaded prior generation")
	unknown := &reloadFault{sd: f.sd, unknown: true}
	um := must(caddy.NewManager(caddyRoot, caddyValidator{f.session}, unknown))
	defer um.Close()
	result, e = um.Apply(f.ctx, main, r.Caddy, caddy.Put(site))
	var ue *caddy.UnknownOutcomeError
	if !errors.As(e, &ue) || result.Outcome != caddy.Unknown || unknown.calls != 1 {
		f.t.Fatal("unknown reload was blindly retried")
	}
	if _, e = um.Observe(); e == nil {
		f.t.Fatal("uncertain manager remained usable")
	}
	observed := must(cm.Observe())
	if observed.Generation != result.Next.Generation || !reflect.DeepEqual(observed.Files, result.Next.Files) {
		f.t.Fatal("unknown disk state differs from recorded intent")
	}
	disk := f.run("caddy", "adapt", "--adapter", "caddyfile", "--config", "/etc/caddy/Caddyfile")
	live := f.run("curl", "--disable", "--noproxy", "*", "--silent", "--fail", "--max-time", "2", "http://127.0.0.1:2019/config/")
	var a, b any
	check(f.t, json.Unmarshal([]byte(disk), &a))
	check(f.t, json.Unmarshal([]byte(live), &b))
	if !reflect.DeepEqual(a, b) {
		f.t.Fatal("unknown reload requires recovery; disk/live configs differ")
	}
	r.Caddy = result.Next
	f.save(*r)
	f.health(r.Plan.HostPort)
	f.t.Log("lost-success acknowledgement injected after real reload; fresh disk and live admin reads reconciled without another mutation")
}

type reloadFault struct {
	sd      *systemd.Client
	calls   int
	unknown bool
}

func (r *reloadFault) Reload(ctx context.Context) error {
	r.calls++
	if r.unknown {
		if e := r.sd.ReloadCaddy(ctx); e != nil {
			return e
		}
		return context.DeadlineExceeded
	}
	if r.calls != 1 {
		return r.sd.ReloadCaddy(ctx)
	}
	link, e := os.Readlink(filepath.Join(caddyRoot, "current"))
	if e != nil {
		return e
	}
	if !strings.HasPrefix(link, "gen-") || strings.ContainsAny(link, "/\\") {
		return fmt.Errorf("unsafe fixture generation")
	}
	path := filepath.Join(caddyRoot, link, "fixture.caddy")
	bytes, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	if e = writeSynced(path, append(append([]byte(nil), bytes...), []byte("invalid_fixture_directive\n")...)); e != nil {
		return e
	}
	reloadErr := r.sd.ReloadCaddy(ctx)
	if e = writeSynced(path, bytes); e != nil {
		return e
	}
	if reloadErr == nil {
		return fmt.Errorf("invalid fixture config unexpectedly accepted")
	}
	return reloadErr
}
func writeSynced(path string, b []byte) error {
	file, e := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0644)
	if e != nil {
		return e
	}
	_, e = file.Write(b)
	if e == nil {
		e = file.Sync()
	}
	return errors.Join(e, file.Close())
}
