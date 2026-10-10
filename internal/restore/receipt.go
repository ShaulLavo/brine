package restore

import (
	"encoding/json"
	"slices"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

// DecodeReceipt refuses unknown and duplicate fields, including nested evidence.
// Successful receipts contain only checked identities and fixed diagnostics.
func DecodeReceipt(raw []byte) (Receipt, error) {
	required := []string{"operation_id", "source", "tool_version", "observed_at", "schema", "loss_window", "integrity_check", "foreign_key_check", "invariant_check"}
	optional := []string{"requested_txid", "recovered_txid", "sentinel", "upload_barrier", "coverage", "position_evidence"}
	f, err := receiptObject(raw, required, optional)
	if err != nil {
		return Receipt{}, err
	}
	if err := receiptSource(f["source"]); err != nil {
		return Receipt{}, err
	}
	if _, err := strictjson.Object(f["schema"], "state", "marker", "catalog_sha256"); err != nil {
		return Receipt{}, err
	}
	if _, err := receiptObject(f["loss_window"], []string{"state", "reason"}, []string{"from", "to"}); err != nil {
		return Receipt{}, err
	}
	if nested, ok := f["sentinel"]; ok {
		if _, err := strictjson.Object(nested, "table", "sequence", "marker", "committed_at"); err != nil {
			return Receipt{}, err
		}
	}
	if nested, ok := f["upload_barrier"]; ok {
		if err := receiptBarrier(nested); err != nil {
			return Receipt{}, err
		}
	}
	if nested, ok := f["coverage"]; ok {
		fields, err := strictjson.Object(nested, "source", "covered_through")
		if err != nil {
			return Receipt{}, err
		}
		if err := receiptSource(fields["source"]); err != nil {
			return Receipt{}, err
		}
	}
	var r Receipt
	if err := json.Unmarshal(raw, &r); err != nil || !r.Valid() {
		return Receipt{}, strictjson.ErrObject
	}
	return r, nil
}
func receiptObject(raw []byte, required, optional []string) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, strictjson.ErrObject
	}
	names := make([]string, 0, len(fields))
	for name, value := range fields {
		if !slices.Contains(required, name) && !slices.Contains(optional, name) || string(value) == "null" {
			return nil, strictjson.ErrObject
		}
		names = append(names, name)
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return nil, strictjson.ErrObject
		}
	}
	return strictjson.Object(raw, names...)
}
func receiptBarrier(raw []byte) error {
	_, err := strictjson.Object(raw, "binding_id", "epoch", "txid", "replica_txid", "observed_at", "succeeded")
	return err
}
func receiptSource(raw []byte) error {
	f, err := receiptObject(raw, []string{"kind"}, []string{"litestream_ltx", "sqlite_snapshot"})
	if err != nil {
		return err
	}
	kind, err := strictjson.Value[SourceKind](f["kind"])
	if err != nil {
		return err
	}
	switch kind {
	case LitestreamLTX:
		if len(f) != 2 || f["litestream_ltx"] == nil {
			return strictjson.ErrObject
		}
		nested, err := receiptObject(f["litestream_ltx"], []string{"binding_id", "epoch", "txid", "recoverability"}, []string{"barrier"})
		if err != nil {
			return err
		}
		if barrier, ok := nested["barrier"]; ok {
			return receiptBarrier(barrier)
		}
	case SQLiteSnapshot:
		if len(f) != 2 || f["sqlite_snapshot"] == nil {
			return strictjson.ErrObject
		}
		_, err := strictjson.Object(f["sqlite_snapshot"], "binding_id", "epoch", "point_id", "object_key", "sha256", "size")
		return err
	default:
		return strictjson.ErrObject
	}
	return nil
}

func (r Receipt) Valid() bool {
	id, epoch, err := r.Source.reference()
	if err != nil || !token.MatchString(id) || !token.MatchString(epoch) || !token.MatchString(r.OperationID) || r.ObservedAt.IsZero() || !validSchema(r.Schema) || r.IntegrityCheck != "passed" || r.ForeignKeyCheck != "passed" || r.InvariantCheck != "passed" && r.InvariantCheck != "unknown" {
		return false
	}
	if r.Source.LTX != nil {
		position := "pinned_cli_exact_plan_and_successful_restore"
		if r.Source.LTX.Recoverability {
			position = "remote_dry_run_and_restore"
		}
		if r.ToolVersion != LitestreamVersion || r.RequestedTXID != r.Source.LTX.TXID || r.RecoveredTXID != r.RequestedTXID || r.PositionEvidence != position {
			return false
		}
	} else if r.ToolVersion != SnapshotToolVersion || r.RequestedTXID != 0 || r.RecoveredTXID != 0 || r.PositionEvidence != "" {
		return false
	}
	if r.Sentinel != nil && (!identifier.MatchString(r.Sentinel.Table) || r.Sentinel.Sequence < 0 || r.Sentinel.Marker == "" || len(r.Sentinel.Marker) > 256 || r.Sentinel.CommittedAt.IsZero()) {
		return false
	}
	if r.Barrier != nil {
		source := r.Source.LTX
		if source == nil || source.Barrier == nil || *r.Barrier != *source.Barrier {
			return false
		}
	}
	switch r.LossWindow.State {
	case LossUnknown:
		return r.Coverage == nil && r.LossWindow.From.IsZero() && r.LossWindow.To.IsZero() && r.LossWindow.Reason == "No independent last-commit coverage proof; asynchronous replication may lose recent writes."
	case LossBounded:
		return r.Coverage != nil && samePoint(r.Coverage.Source, r.Source) && !r.Coverage.CoveredThrough.IsZero() && r.LossWindow.From.Equal(r.Coverage.CoveredThrough) && r.LossWindow.To.Equal(r.ObservedAt) && r.LossWindow.From.Before(r.LossWindow.To) && r.LossWindow.Reason == "Independent source-bound coverage proof bounds possible loss; later writes may be absent."
	default:
		return false
	}
}
