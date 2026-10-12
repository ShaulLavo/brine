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
