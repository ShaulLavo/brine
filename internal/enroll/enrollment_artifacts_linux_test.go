//go:build linux

package enroll

import (
	"os"
	"reflect"
	"testing"

	"github.com/ShaulLavo/brine/internal/workspaceobserve"
)

func TestEnrollmentArtifactRegistry(t *testing.T) {
	artifacts := enrollmentArtifacts()
	want := []struct {
		path  string
		mode  os.FileMode
		class enrollmentArtifactClass
	}{
		{"/usr/local/bin/brine", 0755, artifactUpdatable},
		{"/etc/systemd/system/brine-workspace-observer.service", 0644, artifactUpdatable},
		{"/etc/systemd/user/brine-reconcile.service", 0644, artifactUpdatable},
		{"/etc/systemd/user-generators/brine-reconcile", 0755, artifactUpdatable},
		{"/etc/polkit-1/rules.d/49-brine-caddy.rules", 0644, artifactAuthority},
		{"/etc/ssh/brine/inventory-key", 0644, artifactAuthority},
		{"/etc/ssh/brine/requester", 0644, artifactAuthority},
		{"/etc/ssh/brine/authorized_keys/brine", 0644, artifactAuthority},
		{"/etc/ssh/sshd_config.d/00-brine-brine.conf", 0644, artifactAuthority},
		{"/etc/caddy/Caddyfile", 0644, artifactAuthority},
		{"/etc/ssh/brine/operator-policy.toml", 0644, artifactOperatorOwned},
		{"/etc/ssh/brine/workspace-observer.toml", 0600, artifactOperatorOwned},
		{"/opt/brine/litestream/0.5.17/litestream", 0755, artifactToolPin},
		{"/opt/brine/restic/0.19.1/restic", 0755, artifactToolPin},
		{"/opt/brine/native-reader/brine-native-reader-v1/pin.json", 0644, artifactToolPin},
		{"/etc/brine/runner-storage.conf", 0644, artifactToolPin},
		{admissionDir + "/state.json", 0644, artifactAdmission},
		{admissionDir + "/lock", 0644, artifactAdmission},
		{admissionDir + "/stage.lock", 0644, artifactAdmission},
		{settleUnitPath, 0644, artifactUpdatable},
	}
	if len(artifacts) != len(want) {
		t.Fatal("artifact set changed")
	}
	seen := map[string]bool{}
	for i, artifact := range artifacts {
		expected := want[i]
		if seen[artifact.path] || artifact.path != expected.path || artifact.mode != expected.mode || artifact.class != expected.class {
			t.Fatalf("artifact %d: %+v", i, artifact)
		}
		seen[artifact.path] = true
	}
}

func TestEnrollmentArtifactRenderedBytes(t *testing.T) {
	artifacts := enrollmentArtifacts()
	h := &host{source: []byte("binary\x00\xff"), identityKey: []byte("inventory\x00key"), r: hostRecord{Key: "ssh-ed25519 AAAAfixture"}}
	want := map[enrollmentArtifactID][]byte{
		artifactBinary:             h.source,
		artifactObserverUnit:       []byte(workspaceobserve.Unit),
		artifactReconcileUnit:      []byte(reconcileUnit),
		artifactReconcileGenerator: []byte(reconcileGenerator),
		artifactPolkit:             []byte(policy),
		artifactInventoryKey:       h.identityKey,
		artifactRequester:          []byte("deploy:" + hash([]byte(h.r.Key)) + "\n"),
		artifactDeployKey:          []byte("restrict,command=\"/usr/bin/env BRINE_AUTHENTICATED=deploy /usr/local/bin/brine host serve\" ssh-ed25519 AAAAfixture\n"),
		artifactSSHPolicy:          []byte(sshPolicy),
		artifactObserverGrant:      []byte("allow_workspace_observation = false\n"),
	}
	for id, expected := range want {
		if got := artifacts[id].render(h); !reflect.DeepEqual(got, expected) {
			t.Fatalf("render %s: %q", artifacts[id].path, got)
		}
	}
	for _, id := range []enrollmentArtifactID{artifactCaddyImport, artifactOperatorPolicy, artifactLitestream, artifactRestic, artifactNativeReader, artifactRunnerStorage} {
		if artifacts[id].render != nil {
			t.Fatalf("externally installed artifact gained a renderer: %s", artifacts[id].path)
		}
	}
}

func TestEnrollmentArtifactRenderingUsesCurrentInputs(t *testing.T) {
	artifacts := enrollmentArtifacts()
	h := &host{}
	h.source = []byte("new source")
	h.identityKey = []byte("new identity")
	h.r.Key = "new deploy key"
	if string(artifacts[artifactBinary].render(h)) != "new source" || string(artifacts[artifactInventoryKey].render(h)) != "new identity" {
		t.Fatal("registry captured empty host inputs")
	}
	if string(artifacts[artifactRequester].render(h)) != "deploy:"+hash([]byte(h.r.Key))+"\n" {
		t.Fatal("registry captured the old requester")
	}
}

func TestEnrollmentArtifactBytePins(t *testing.T) {
	artifacts := enrollmentArtifacts()
	want := map[enrollmentArtifactID]string{
		artifactObserverUnit:       "8efb47fa05e7d7123589fb84bdc6c7727148a1f3dd32a6ae85dcab5b85f4f38c",
		artifactReconcileUnit:      "bceef76cbd4a021c8e857af5818f871629240b4619a321cfd8b3881f11d82401",
		artifactReconcileGenerator: "94fc55cb8fbea00607e12d1e67abdede0549437e2f9ebf18bb9f3dc8669550cf",
		artifactPolkit:             "e069cd2f4e3e8e7cfa25b3db0086e6f90e70e853febf78750f6a2e57b7f2cc97",
	}
	for id, expected := range want {
		if got := hash(artifacts[id].render(&host{})); got != expected {
			t.Fatalf("rendered artifact changed: %s %s", artifacts[id].path, got)
		}
	}
}
