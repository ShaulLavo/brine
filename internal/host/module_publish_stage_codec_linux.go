//go:build linux

package host

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/spacetimedb"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

func decodeModuleStageObservation(raw []byte, startedAt, finishedAt time.Time) (moduleStageObservation, error) {
	var observation moduleStageObservation
	if len(raw) == 0 || len(raw) > 1<<20 {
		return observation, datapublish.ErrRecord
	}
	fields := []string{"action", "credential_sha256", "publisher", "publisher_proof", "preflight", "receipt", "receipt_binding_sha256", "database", "program_wire_hash", "schema_sha256", "publication_known", "child_stopped"}
	if _, err := strictjson.Object(raw, fields...); err != nil || json.Unmarshal(raw, &observation) != nil || !observation.ChildStopped || !data.ValidCatalogHash(observation.CredentialSHA256) || !data.ValidCatalogHash(observation.Publisher) || len(observation.PublisherProof) == 0 || len(observation.PublisherProof) > 8192 || len(observation.Preflight) > datapublish.NativePreflightResponseLimit || len(observation.Receipt) > datapublish.NativePreflightResponseLimit {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	if checkModuleStagePublisherProof(observation, startedAt, finishedAt) != nil {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	if observation.Action == moduleStagePublisher {
		if observation.Database != "" || observation.ProgramWireHash != "" || observation.SchemaSHA256 != "" || len(observation.Preflight) != 0 || len(observation.Receipt) != 0 || observation.ReceiptBindingSHA != "" || observation.PublicationKnown {
			return moduleStageObservation{}, datapublish.ErrRecord
		}
		return observation, nil
	}
	if observation.Action != moduleStagePreflight && observation.Action != moduleStagePublish && observation.Action != moduleStageObserve || !data.ValidCatalogHash(observation.Database) || !data.ValidCatalogHash(observation.ProgramWireHash) || !data.ValidCatalogHash(observation.SchemaSHA256) {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	if observation.PublicationKnown && (len(observation.Receipt) == 0 || !data.ValidCatalogHash(observation.ReceiptBindingSHA)) {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	if observation.Action != moduleStagePublish && (observation.PublicationKnown || len(observation.Receipt) != 0 || observation.ReceiptBindingSHA != "") {
		return moduleStageObservation{}, datapublish.ErrRecord
	}
	return observation, nil
}

func checkModuleStagePublisherProof(observation moduleStageObservation, startedAt, finishedAt time.Time) error {
	server, err := spacetimedb.NewPrivateServerEndpoint("127.0.0.1", privateEnginePort)
	if err != nil || startedAt.IsZero() || finishedAt.IsZero() || finishedAt.Before(startedAt) {
		return datapublish.ErrRecord
	}
	fields, err := strictjson.Object(observation.PublisherProof, "purpose", "endpoint_sha256", "method", "path", "status", "publisher", "credential_sha256", "verified_at")
	if err != nil {
		return datapublish.ErrRecord
	}
	var proof struct {
		Purpose       string `json:"purpose"`
		Method        string `json:"method"`
		Path          string `json:"path"`
		Status        int    `json:"status"`
		Publisher     string `json:"publisher"`
		CredentialSHA string `json:"credential_sha256"`
	}
	if json.Unmarshal(observation.PublisherProof, &proof) != nil || proof.Purpose != "native_supplied_key_publisher_verification" || proof.Method != http.MethodGet || proof.Path != "/v1/identity/"+observation.Publisher+"/verify" || proof.Status != http.StatusNoContent || proof.Publisher != observation.Publisher || proof.CredentialSHA != observation.CredentialSHA256 {
		return datapublish.ErrRecord
	}
	endpoint, err := strictjson.Value[string](fields["endpoint_sha256"])
	if err != nil || endpoint != server.SHA256() {
		return datapublish.ErrRecord
	}
	stamp, err := strictjson.Value[string](fields["verified_at"])
	if err != nil || stamp == "" || !strings.HasSuffix(stamp, "Z") {
		return datapublish.ErrRecord
	}
	verifiedAt, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil || verifiedAt.IsZero() || verifiedAt.Before(startedAt) || verifiedAt.After(finishedAt) {
		return datapublish.ErrRecord
	}
	return nil
}
