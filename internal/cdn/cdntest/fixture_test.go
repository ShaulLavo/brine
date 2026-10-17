package cdntest_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdn/cdntest"
)

func apply(t *testing.T, driver *cdntest.Driver, attachment cdn.Attachment, id string) cdn.Permit {
	t.Helper()
	ctx := t.Context()
	observed, err := driver.Observe(ctx, attachment)
	if err != nil {
		t.Fatal(err)
	}
	steps, err := driver.Actions(attachment, observed)
	if err != nil || len(steps) != 1 {
		t.Fatalf("actions: %v %v", steps, err)
	}
	sealed, err := driver.Seal(ctx, attachment, steps[0], cdn.OwnedReceipt{})
	if err != nil {
		t.Fatal(err)
	}
	permit := cdn.Permit{Operation: cdn.OperationID(id), Effect: cdn.EffectID(id + "-effect"), Claim: "writer", Action: sealed.Action}
	result, err := driver.Execute(ctx, permit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(cdn.Acknowledged); !ok {
		t.Fatalf("not acknowledged: %#v", result)
	}
	complete(t, driver, permit)
	return permit
}

func complete(t *testing.T, driver *cdntest.Driver, permit cdn.Permit) cdn.Completed {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		inspection, err := driver.Inspect(t.Context(), permit)
		if err != nil {
			t.Fatal(err)
		}
		if completed, ok := inspection.(cdn.Completed); ok {
			return completed
		}
		select {
		case <-deadline.C:
			t.Fatal("native operation did not complete")
		case <-time.After(time.Millisecond):
		}
	}
}

func get(t *testing.T, f *cdntest.Fixture, path string, headers http.Header) (http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, f.GatewayURL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "game.example.test"
	request.Header = headers.Clone()
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	request.Header.Set("Cache-Control", "no-cache, no-store")
	request.Header.Set("Pragma", "no-cache")
	response, err := f.Client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.Header, body
}

func TestStableURLSwitchEvictionAndDisableUseActualBytes(t *testing.T) {
	f := cdntest.New(t)
	a := f.Attachment(f.EdgeA.Profile().Reference)
	apply(t, f.EdgeA, a, "prepare-a")
	apply(t, f.Traffic, a, "traffic-a")
	header, first := get(t, f, f.Sample.Path, nil)
	if header.Get("X-CDN-Cache") != "MISS" || cdn.Hash(first) != f.Sample.Digest {
		t.Fatal("first full GET did not use origin")
	}
	header, second := get(t, f, f.Sample.Path, nil)
	if header.Get("X-CDN-Cache") != "HIT" || cdn.Hash(second) != f.Sample.Digest || f.OriginHits(f.Sample.Path) != 1 {
		t.Fatal("later full GET did not hit cache")
	}
	b := f.Attachment(f.EdgeB.Profile().Reference)
	apply(t, f.EdgeB, b, "prepare-b")
	apply(t, f.Traffic, b, "traffic-b")
	header, _ = get(t, f, f.Sample.Path, nil)
	if header.Get("X-CDN-Endpoint") != "edge-b" || header.Get("X-CDN-Cache") != "MISS" {
		t.Fatal("independent second cache not selected")
	}
	get(t, f, f.Sample.Path, nil)
	f.Evict(f.EdgeB.Profile().Reference)
	header, body := get(t, f, f.Sample.Path, nil)
	if header.Get("X-CDN-Cache") != "MISS" || cdn.Hash(body) != f.Sample.Digest || f.OriginHits(f.Sample.Path) != 3 {
		t.Fatal("evicted object did not come from real origin")
	}
	proof, err := f.EdgeB.Probe(t.Context(), cdn.Qualification{Attachment: b, Sample: f.Sample})
	if err != nil || !proof.LaterHit || !proof.HashesVerified || !proof.EdgeHealthy || proof.Method != cdn.OperatorHTTP {
		t.Fatalf("probe: %#v %v", proof, err)
	}
	b.Mode = cdn.Direct
	apply(t, f.Traffic, b, "traffic-direct")
	header, body = get(t, f, f.Sample.Path, nil)
	if header.Get("X-CDN-Endpoint") != "direct" || header.Get("X-CDN-Cache") != "BYPASS" || cdn.Hash(body) != f.Sample.Digest {
		t.Fatal("disable did not reach direct origin")
	}
}

func TestUnknownReplyPendingWriteNeedsNativeCompletion(t *testing.T) {
	f := cdntest.New(t)
	a := f.Attachment(f.EdgeA.Profile().Reference)
	observed, _ := f.EdgeA.Observe(t.Context(), a)
	steps, _ := f.EdgeA.Actions(a, observed)
	sealed, err := f.EdgeA.Seal(t.Context(), a, steps[0], cdn.OwnedReceipt{})
	if err != nil {
		t.Fatal(err)
	}
	gate := f.DelayNext(f.EdgeA.Profile().Reference)
	f.DropReply(f.EdgeA.Profile().Reference)
	effect := cdn.Effect{Stage: cdn.Planned, Permit: cdn.Permit{Operation: "delayed", Effect: "delayed-effect", Action: sealed.Action}}
	effect, work, _ := effect.Claim("original")
	permit := work.(cdn.NeedEffect).Permit
	result, err := f.EdgeA.Execute(t.Context(), permit)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.(cdn.Uncertain); !ok {
		t.Fatalf("lost reply: %#v", result)
	}
	<-gate.Started
	effect, err = effect.Record(result)
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := f.EdgeA.Inspect(t.Context(), permit)
	if err != nil {
		t.Fatal(err)
	}
	_, next, err := effect.Inspect(inspection)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := next.(cdn.Waiting); !ok {
		t.Fatal("absent/before readback falsely completed delayed write")
	}
	_, second, _ := effect.Claim("second")
	if _, ok := second.(cdn.NeedObservation); !ok {
		t.Fatal("second writer got a mutation permit")
	}
	close(gate.Release)
	completed := complete(t, f.EdgeA, permit)
	effect, _, err = effect.Inspect(completed)
	if err != nil || effect.Stage != cdn.Confirmed || f.NativeWrites(f.EdgeA.Profile().Reference) != 1 {
		t.Fatalf("completion: %#v %v", effect, err)
	}
}

func TestBypassAndForeignOwnership(t *testing.T) {
	f := cdntest.New(t)
	a := f.Attachment(f.EdgeA.Profile().Reference)
	apply(t, f.EdgeA, a, "prepare")
	apply(t, f.Traffic, a, "traffic")
	for _, headers := range []http.Header{{"Authorization": {"private"}}, {"Cookie": {"private"}}, {"Range": {"bytes=0-1"}}} {
		header, _ := get(t, f, f.Sample.Path, headers)
		if header.Get("X-CDN-Cache") != "BYPASS" {
			t.Fatal("private/range request cached")
		}
	}
	for _, path := range []string{"/pack/index.json", f.Sample.Path + "?query=1", "/api/save", "/pack/blobs/" + strings.Repeat("a", 64) + ".bin"} {
		header, _ := get(t, f, path, nil)
		if header.Get("X-CDN-Cache") != "BYPASS" {
			t.Fatal("mutable/error/query request cached")
		}
	}
	for _, header := range []http.Header{{"Content-Type": {"text/html"}}, {"Content-Type": {"application/xhtml+xml"}}, {"Cache-Control": {"private"}}, {"Cache-Control": {"no-store"}}, {"Set-Cookie": {"session=private"}}, {"Content-Encoding": {"br"}}} {
		assertResponseBypass(t, f, header)
	}
	f.Foreign(f.EdgeB.Profile().Reference)
	b := f.Attachment(f.EdgeB.Profile().Reference)
	observed, err := f.EdgeB.Observe(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.EdgeB.Actions(b, observed); err == nil {
		t.Fatal("foreign resource adopted")
	}
}

func assertResponseBypass(t *testing.T, f *cdntest.Fixture, header http.Header) {
	t.Helper()
	response := cdntest.Response{Status: 200, Header: http.Header{"Content-Type": {"application/octet-stream"}, "Cache-Control": {"public, max-age=31536000, immutable"}}, Body: []byte("synthetic immutable fixture bytes\n")}
	for key, values := range header {
		response.Header[key] = values
	}
	f.SetOrigin(f.Sample.Path, response)
	f.Evict(f.EdgeA.Profile().Reference)
	for range 2 {
		actual, _ := get(t, f, f.Sample.Path, nil)
		if actual.Get("X-CDN-Cache") != "BYPASS" {
			t.Fatalf("response cached: %v", header)
		}
	}
}
