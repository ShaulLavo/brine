package store

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/restic"
)

func nativeEmptyBundleRecords(t *testing.T) (nativePreparedPoint, []byte, []byte, []byte) {
	t.Helper()
	binding, epoch, pointID, generation := strings.Repeat("1", 32), strings.Repeat("2", 32), strings.Repeat("3", 32), strings.Repeat("4", 32)
	config, key := strings.Repeat("a", 64), strings.Repeat("b", 64)
	source, err := nativecapture.UnpublishedSource(nativecapture.EmptyRecord{Format: nativecapture.Format, Scope: nativecapture.EmptyScope{BindingID: binding, EpochID: epoch}, GenerationID: generation, ConfigurationSHA256: config, PublisherReference: "publisher", SigningRecovery: nativecapture.SigningRecovery{Kind: "external_immutable", Reference: "signing", Version: 1, SHA256: key}, Members: []nativecapture.CheckpointMember{{Path: "config.toml", Bytes: 10, SHA256: config}}}, nativecapture.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	sourceRaw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	identity := restic.Identity{BindingID: binding, EpochID: epoch, CaptureID: pointID, ModuleSHA256: source.SHA256(), CheckpointSHA256: source.SHA256()}
	members := []restic.Member{{Path: "config.toml", Type: "file", Mode: 0600, Size: 10, SHA256: config}, {Path: "native-source.json", Type: "file", Mode: 0600, Size: int64(len(sourceRaw)), SHA256: source.SHA256()}}
	manifestRaw, err := json.Marshal(struct {
		Version  int             `json:"version"`
		Identity restic.Identity `json:"identity"`
		Members  []restic.Member `json:"members"`
	}{1, identity, members})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := restic.DecodeManifest(manifestRaw, restic.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	transport := restic.TransportPoint{Identity: identity, Scope: restic.Scope{BindingID: binding, EpochID: epoch, RepositoryID: strings.Repeat("c", 64), DestinationSHA256: strings.Repeat("d", 64), CredentialVersion: 1, RecoveryVersion: 1, ExpiresAt: time.Now().UTC().Add(time.Minute)}, SnapshotID: strings.Repeat("e", 64), ManifestSHA256: manifest.SHA256(), ArchiveSHA256: strings.Repeat("f", 64), ArchiveBytes: 10000}
	transportRaw, err := json.Marshal(transport)
	if err != nil {
		t.Fatal(err)
	}
	point := nativePreparedPoint{view: NativePreparedPointView{PointID: pointID, BindingID: data.ReplicaBindingID(binding), EpochID: data.ReplicaEpochID(epoch), SourceKind: "unpublished", SourceSHA256: source.SHA256(), CheckpointSHA256: source.SHA256(), ConfigSHA256: config, SigningRecoverySHA256: nativeSigningRecoverySHA256(nativecapture.SigningRecovery{Kind: "external_immutable", Reference: "signing", Version: 1, SHA256: key}), GenerationID: generation, BundleManifestSHA256: manifest.SHA256(), TransportPointSHA256: nativePointHash(transportRaw)}}
	return point, sourceRaw, manifestRaw, transportRaw
}

func TestNativePreparedEmptyBundleUsesExactCanonicalSourceCheckpoint(t *testing.T) {
	point, source, manifest, transport := nativeEmptyBundleRecords(t)
	bundle, err := decodeNativePreparedBundle(context.Background(), nil, point, source, manifest, transport, nil, nil, nil, nil)
	if err != nil || bundle.Source().Kind() != "unpublished" {
		t.Fatal("exact empty bundle refused", err)
	}
	point.view.CheckpointSHA256 = strings.Repeat("9", 64)
	if _, err := decodeNativePreparedBundle(context.Background(), nil, point, source, manifest, transport, nil, nil, nil, nil); err == nil {
		t.Fatal("unrelated empty checkpoint admitted")
	}
}

func TestNativePreparedEmptyBundleRejectsInitializedEvidence(t *testing.T) {
	point, source, manifest, transport := nativeEmptyBundleRecords(t)
	for _, records := range [][]byte{[]byte(`{"native":"syntax"}`), []byte(`[]`)} {
		if _, err := decodeNativePreparedBundle(context.Background(), nil, point, source, manifest, transport, records, nil, nil, nil); err == nil {
			t.Fatal("empty bundle acquired native history")
		}
		if _, err := decodeNativePreparedBundle(context.Background(), nil, point, source, manifest, transport, nil, nil, nil, records); err == nil {
			t.Fatal("empty bundle acquired final pairs")
		}
	}
}

func TestNativePreparedEmptyBundleRejectsCoherentlyRehashedUnrelatedModule(t *testing.T) {
	point, source, manifestRaw, transportRaw := nativeEmptyBundleRecords(t)
	var document map[string]json.RawMessage
	if err := json.Unmarshal(manifestRaw, &document); err != nil {
		t.Fatal(err)
	}
	var identity restic.Identity
	if err := json.Unmarshal(document["identity"], &identity); err != nil {
		t.Fatal(err)
	}
	identity.ModuleSHA256 = strings.Repeat("9", 64)
	document["identity"], _ = json.Marshal(identity)
	manifestRaw, _ = json.Marshal(document)
	manifest, err := restic.DecodeManifest(manifestRaw, restic.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	var transport restic.TransportPoint
	if err := json.Unmarshal(transportRaw, &transport); err != nil {
		t.Fatal(err)
	}
	transport.Identity, transport.ManifestSHA256 = identity, manifest.SHA256()
	transportRaw, _ = json.Marshal(transport)
	point.view.BundleManifestSHA256, point.view.TransportPointSHA256 = manifest.SHA256(), nativePointHash(transportRaw)
	if _, err := decodeNativePreparedBundle(context.Background(), nil, point, source, manifestRaw, transportRaw, nil, nil, nil, nil); err == nil {
		t.Fatal("rehashed wrong empty module identity admitted")
	}
}

func TestNativePreparedCompletionRequiresRealStoppedAndAuthenticatedExtraction(t *testing.T) {
	state := &Store{}
	if _, err := state.CompleteNativePreparedPoint(context.Background(), nil, restic.AuthenticatedRestore{}, NativePreparedObservation{}); err == nil {
		t.Fatal("missing actual stopped capability completed point")
	}
	if _, err := state.ReadNativePreparedBundle(context.Background(), nil); err == nil {
		t.Fatal("missing actual stopped capability read bundle")
	}
	if (&NativeRepositoryRead{}).AdmitRepository(context.Background(), restic.Scope{}, restic.CreateRepository, "") == nil {
		t.Fatal("read capability created repository")
	}
}

func TestNativePreparedEmptyBundleRejectsCrossedSigningRecovery(t *testing.T) {
	point, source, manifest, transport := nativeEmptyBundleRecords(t)
	point.view.SigningRecoverySHA256 = strings.Repeat("9", 64)
	if _, err := decodeNativePreparedBundle(context.Background(), nil, point, source, manifest, transport, nil, nil, nil, nil); err == nil {
		t.Fatal("authenticated source did not bind signing recovery")
	}
}
