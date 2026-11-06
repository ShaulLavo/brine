package inventory

import (
	"context"
	"github.com/ShaulLavo/brine/internal/localexec"
	"strings"
	"testing"

	"github.com/ShaulLavo/brine/internal/target"
)

func TestReadinessRuntimeCollectorsAndCaddyClassifier(t *testing.T) {
	for _, test := range []struct {
		raw             string
		catch, loopback bool
	}{
		{`{"apps":{"http":{"servers":{"s":{"routes":[{"match":[{"host":["app.example.com"]}]}]}}}}}`, false, true},
		{`{"admin":{"listen":"0.0.0.0:2019"},"apps":{"http":{"servers":{"s":{"routes":[{}]}}}}}`, true, false},
	} {
		testReadinessRuntime(t, test.raw, test.catch, test.loopback)
	}
}
func testReadinessRuntime(t *testing.T, raw string, catch, loopback bool) {
	t.Helper()
	runner := fakeRunner{
		"stat --format=%a /usr/bin/newuidmap /usr/bin/newgidmap":                                "4755\n4755",
		"dpkg-query -W -f=${db:Status-Status} dbus-user-session":                                "installed",
		"dpkg-query -W -f=${db:Status-Status} polkitd":                                          "installed",
		"systemctl --user show --property=Version --value":                                      "257.8",
		"systemctl show caddy.service --property=ActiveState --value":                           "active",
		"curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": raw,
		"caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile":                         raw,
	}
	r, err := (Collector{Runner: runner}).Readiness(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, fact := range []target.Observation[bool]{r.UIDMap, r.DBus, r.Polkit, r.UserBus, r.CaddyActive} {
		if fact.Value == nil || !*fact.Value {
			t.Fatal(r)
		}
	}
	if r.CatchAll.Value == nil || *r.CatchAll.Value != catch || r.AdminLoopback.Value == nil || *r.AdminLoopback.Value != loopback {
		t.Fatal(r)
	}
	delete(runner, "curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/")
	runner["caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"] = `{}`
	r, err = (Collector{Runner: runner}).Readiness(context.Background())
	if err != nil || r.AdminLoopback.Value != nil {
		t.Fatal("disk healthy fallback hid failed live read", r, err)
	}
}
func TestReadinessStrictRuntimeParsers(t *testing.T) {
	for _, text := range []string{"4755\n4755", "755\n4755", "bad\n4755", "4755"} {
		testReadinessHelperMode(t, text)
	}
	if activeState("planted-secret").Value != nil {
		t.Fatal("raw status admitted")
	}
	if activeState("inactive").Value == nil || *activeState("inactive").Value {
		t.Fatal("inactive accepted")
	}
}

func testReadinessHelperMode(t *testing.T, text string) {
	t.Helper()
	value := helperModes(text)
	switch text {
	case "4755\n4755":
		if value.Value == nil || !*value.Value {
			t.Fatal(value)
		}
	case "755\n4755":
		if value.Value == nil || *value.Value {
			t.Fatal(value)
		}
	default:
		if value.Value != nil {
			t.Fatal(value)
		}
	}
}

type readinessCapture struct {
	t      *testing.T
	runner fakeRunner
}

func (r readinessCapture) CaptureStdout(ctx context.Context, limit int, path string, args ...string) (localexec.Capture, error) {
	if path == "podman" && strings.Join(args, " ") != "version --format json" {
		r.t.Fatal("application storage command during readiness", args)
	}
	return r.runner.CaptureStdout(ctx, limit, path, args...)
}
func TestReadinessInventorySkipsPodmanStorageOpening(t *testing.T) {
	fs := baseFixture()
	fs.files["/etc/passwd"] = "brine:x:1001:1001::/home/brine:/bin/bash\n"
	fs.files["/proc/self/status"] = "Uid:\t1001\t1001\t1001\t1001\n"
	runner := readinessCapture{t: t, runner: fakeRunner{"uname -m": "aarch64", "podman version --format json": `{"Client":{"Version":"5.4.2"}}`}}
	c := Collector{FS: fs, Runner: runner, IdentityKey: []byte("fixture")}
	snapshot, err := c.CollectReadiness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.OS.ID != "debian" || snapshot.Apps.Value != nil {
		t.Fatal(snapshot)
	}
}
