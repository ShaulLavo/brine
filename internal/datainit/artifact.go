package datainit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const ArtifactLimit = 1 << 20
const PlanLimit = 4 << 20

// DecodeArtifact checks the exact reviewed bytes, not a re-encoded substitute.
// SQLite recompilation remains an independent planning and apply-time check.
func DecodeArtifact(raw []byte, id string) (data.SchemaInitializer, error) {
	var initializer data.SchemaInitializer
	if !ValidID(id) || len(raw) == 0 || len(raw) > ArtifactLimit {
		return initializer, ErrRefused
	}
	sum := sha256.Sum256(raw)
	if "sha256:"+hex.EncodeToString(sum[:]) != id {
		return initializer, ErrRefused
	}
	fields, err := strictjson.Object(raw, "definition", "statements")
	if err != nil {
		return initializer, ErrRefused
	}
	if _, err := strictjson.Object(fields["definition"], "database", "marker", "catalog_sha256"); err != nil {
		return initializer, ErrRefused
	}
	if json.Unmarshal(raw, &initializer) != nil {
		return initializer, ErrRefused
	}
	return initializer, nil
}
