//go:build linux

package host

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/podman"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
)

func TestModuleStageRequiresActualEmptySourceAndBlocksOrdinaryHost(t *testing.T) {
	if _, err := acquireInitialPublisherStage(context.Background(), engineEmptySource{}, nil); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("syntax manufactured an initial private source", err)
	}
	if _, err := runInitialPublisherStage(context.Background(), &publicationStageLease{}, nil, localexec.Session{}); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("unowned stage granted publisher evidence", err)
	}
	if err := RunModuleStage(context.Background(), strings.NewReader("{}"), &bytes.Buffer{}); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("ordinary host could run private publication purpose", err)
	}
}

func TestModuleStagePublisherInputCannotSelectDatabaseOrMutation(t *testing.T) {
	permit, _ := engineStartupFixture(t)
	runtime := permit.Runtime
	runtime.State, runtime.Database, runtime.Publisher, runtime.Names = data.EngineUnpublished, "", "", []string{}
	request := moduleStageInput{Action: moduleStagePublisher, Runtime: runtime, Token: "header.payload.signature"}
	raw, _ := json.Marshal(request)
	if _, err := readModuleStageInput(bytes.NewReader(raw)); err != nil {
		t.Fatal("declared unpublished runtime cannot verify its existing supplied credential", err)
	}
	request.Action = moduleStagePublish
	raw, _ = json.Marshal(request)
	if _, err := readModuleStageInput(bytes.NewReader(raw)); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("initial role verification became module publication", err)
	}
	request.Action, request.WasmSHA256 = moduleStagePublisher, strings.Repeat("a", 64)
	raw, _ = json.Marshal(request)
	if _, err := readModuleStageInput(bytes.NewReader(raw)); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("publisher verification accepted candidate mutation material", err)
	}
	request.WasmSHA256, request.Runtime.Secrets.Publisher.ExpiresAt = "", time.Now().UTC().Add(time.Second)
	raw, _ = json.Marshal(request)
	if _, err := readModuleStageInput(bytes.NewReader(raw)); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("expiring supplied role escaped native stage budget", err)
	}
}

func TestModuleStageMountsPermitOnlyPrivateCopyAndReadonlyKeys(t *testing.T) {
	valid := strings.Replace(descriptorMountFixture(), "2 1 0:2 / /input/module.wasm ro - ext4 artifact ro\n", "", 1) +
		"8 1 0:8 / /data rw - ext4 copy rw\n9 1 0:9 / /keys/private.pem ro - ext4 key ro\n10 1 0:10 / /keys/public.pem ro - ext4 key ro\n"
	if !moduleStageMounts([]byte(valid)) {
		t.Fatal("closed private stage mount profile refused")
	}
	for _, attack := range []string{strings.Replace(valid, "/keys/private.pem ro", "/keys/private.pem rw", 1), strings.Replace(valid, "/data rw", "/data ro", 1), valid + "11 1 0:11 / /input/module.wasm rw - ext4 artifact rw\n", valid + "11 1 0:11 / /work ro - ext4 original ro\n", valid + "11 1 0:11 / /credentials ro - ext4 credentials ro\n"} {
		if moduleStageMounts([]byte(attack)) {
			t.Fatal("wrong private copy/key/artifact/credential mount admitted")
		}
	}
}

func TestModuleStagePublisherProofBindsExactEndpointAndAttemptInterval(t *testing.T) {
	started := time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC)
	finished := started.Add(time.Minute)
	server, _ := spacetimedb.NewPrivateServerEndpoint("127.0.0.1", privateEnginePort)
	publisher, credential := strings.Repeat("a", 64), strings.Repeat("b", 64)
	proof := map[string]any{"purpose": "native_supplied_key_publisher_verification", "endpoint_sha256": server.SHA256(), "method": "GET", "path": "/v1/identity/" + publisher + "/verify", "status": 204, "publisher": publisher, "credential_sha256": credential, "verified_at": started.Add(time.Second)}
	observation := moduleStageObservation{Action: moduleStagePublisher, Publisher: publisher, CredentialSHA256: credential, ChildStopped: true}
	observation.PublisherProof, _ = json.Marshal(proof)
	raw, _ := json.Marshal(observation)
	if _, err := decodeModuleStageObservation(raw, started, finished); err != nil {
		t.Fatal("exact fresh publisher transcript refused", err)
	}
	attacks := map[string]any{"endpoint_sha256": strings.Repeat("c", 64), "publisher": strings.Repeat("d", 64), "credential_sha256": strings.Repeat("e", 64), "status": 200, "method": "POST", "path": "/v1/identity", "verified_at": started.Add(-time.Nanosecond)}
	for field, value := range attacks {
		t.Run(field, func(t *testing.T) {
			testModuleStagePublisherProofAttack(t, observation, proof, field, value, started, finished)
		})
	}
	testModuleStagePublisherProofAttack(t, observation, proof, "verified_at", finished.Add(time.Nanosecond), started, finished)
	if _, err := decodeModuleStageObservation(raw, time.Time{}, finished); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("missing actual start time accepted", err)
	}
}

func testModuleStagePublisherProofAttack(t *testing.T, observation moduleStageObservation, proof map[string]any, field string, value any, started, finished time.Time) {
	t.Helper()
	original := proof[field]
	proof[field] = value
	observation.PublisherProof, _ = json.Marshal(proof)
	proof[field] = original
	raw, _ := json.Marshal(observation)
	if _, err := decodeModuleStageObservation(raw, started, finished); !errors.Is(err, datapublish.ErrRecord) {
		t.Fatal("wrong/stale publisher transcript admitted", field, err)
	}
}

func TestModuleStageIndependentlyRequiresNoSwap(t *testing.T) {
	container, permit, material := ownedEngineFixture(t)
	lease := &publicationStageLease{permit: permit, material: material, image: podman.ImageInfo{ImageID: permit.Runtime.ImageID}, nonce: strings.Repeat("a", 32), stagePath: "/private/stage"}
	container.Name, container.Path, container.Args = "brine-module-stage-"+lease.nonce, "/brine", []string{"host", "module-stage"}
	container.Config.Labels = map[string]string{"BRINE_PRIVATE_VERIFY": lease.nonce, "BRINE_MODULE_STAGE": lease.nonce}
	container.HostConfig.NetworkMode = "none"
	container.Mounts[0].Source = lease.stagePath
	container.Mounts = append(container.Mounts, engineMount{Source: "/usr/local/bin/brine", Destination: "/brine", Type: "bind", RW: engineBool(false)})
	if ownedPublicationStage(lease, container) {
		t.Fatal("missing MemorySwap observation admitted")
	}
	memory := int64(permit.Runtime.MemoryMB) << 20
	container.HostConfig.MemorySwap = &memory
	if !ownedPublicationStage(lease, container) {
		t.Fatal("exact no-swap private stage refused")
	}
	for _, swap := range []int64{-1, 0, memory + 1} {
		container.HostConfig.MemorySwap = &swap
		if ownedPublicationStage(lease, container) {
			t.Fatal("wrong swap budget admitted", swap)
		}
	}
}
