package inventory

import (
	"testing"
)

func TestReviewActiveCatchAllCannotBeHiddenByDiskConfig(t *testing.T) {
	live := `{"apps":{"http":{"servers":{"s":{"routes":[{}]}}}}}`
	disk := `{"apps":{"http":{"servers":{"s":{"routes":[{"match":[{"host":["app.example.com"]}]}]}}}}}`
	c := Collector{Runner: fakeRunner{
		"curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/": live,
		"caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile":                         disk,
	}}
	r, err := c.Readiness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live=%s disk=%s catch_all=%+v", live, disk, r.CatchAll)
	if r.CatchAll.Value != nil && !*r.CatchAll.Value {
		t.Fatal("active catch-all reported absent from disk-only configuration")
	}
}

func TestReadinessCatchAllRequiresBothAbsenceObservations(t *testing.T) {
	const exact = `{"apps":{"http":{"servers":{"s":{"routes":[{"match":[{"host":["app.example.com"]}]}]}}}}}`
	const hostless = `{"apps":{"http":{"servers":{"s":{"routes":[{}]}}}}}`
	for _, test := range []struct {
		live, disk, want string
	}{
		{hostless, exact, "present"}, {exact, hostless, "present"},
		{exact, exact, "absent"}, {"", exact, "unknown"},
		{"invalid", exact, "unknown"}, {"null", exact, "unknown"},
		{hostless, "", "present"}, {"", hostless, "present"},
		{"", "", "unknown"},
	} {
		checkReadinessCatchAllObservations(t, test.live, test.disk, test.want)
	}
}

func checkReadinessCatchAllObservations(t *testing.T, live, disk, want string) {
	t.Helper()
	runner := fakeRunner{}
	if live != "" {
		runner["curl --disable --noproxy * --silent --fail --max-time 2 http://127.0.0.1:2019/config/"] = live
	}
	if disk != "" {
		runner["caddy adapt --config /etc/caddy/Caddyfile --adapter caddyfile"] = disk
	}
	r, err := (Collector{Runner: runner}).Readiness(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if want == "unknown" {
		if r.CatchAll.Value != nil {
			t.Fatal(live, disk, r.CatchAll)
		}
		return
	}
	if r.CatchAll.Value == nil || *r.CatchAll.Value != (want == "present") {
		t.Fatal(live, disk, want, r.CatchAll)
	}
}
