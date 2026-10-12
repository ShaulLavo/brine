package nativecapture

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBackupResolutionReviewIsClosedAndBounded(t *testing.T) {
	now := time.Now().UTC()
	r := BackupResolutionReview{Format: "brine-native-backup-resolution-review-v1", OriginalOperationID: "original", OriginalKind: "native_capture", Requester: "operator", BindingID: strings.Repeat("1", 32), OriginalSHA256: strings.Repeat("2", 64), ObservationSHA256: strings.Repeat("3", 64), IssuedAt: now, ExpiresAt: now.Add(10 * time.Minute)}
	identity, _ := json.Marshal(r)
	r.ID = sourceDigest(identity)
	raw, _ := json.Marshal(r)
	if _, err := DecodeBackupResolutionReview(raw); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Replace(string(raw), `"native_capture"`, `"deploy"`, 1), strings.Replace(string(raw), r.OriginalSHA256, strings.Repeat("4", 64), 1), strings.Replace(string(raw), `"id":`, `"path":"/private","id":`, 1)} {
		if _, err := DecodeBackupResolutionReview([]byte(invalid)); err == nil {
			t.Fatal("crossed/unscoped review admitted")
		}
	}
}
func TestBackupResolutionReceiptCannotChangeOriginalKindOrScope(t *testing.T) {
	original := BackupPolicyReceipt{Kind: "native_backup_activate", OperationID: "original", BindingID: strings.Repeat("1", 32), EpochID: strings.Repeat("2", 32), PlanID: strings.Repeat("3", 32), Revision: 1, PolicySHA256: strings.Repeat("4", 64), RepositoryID: strings.Repeat("5", 64), Outcome: "active"}
	originalRaw, _ := json.Marshal(original)
	r := BackupResolutionReceipt{Kind: "native_backup_resolve", OperationID: "resolver", ReviewID: strings.Repeat("6", 64), OriginalOperationID: original.OperationID, OriginalKind: original.Kind, BindingID: original.BindingID, EpochID: original.EpochID, OriginalSHA256: strings.Repeat("7", 64), ObservationSHA256: strings.Repeat("8", 64), CompletionSHA256: sourceDigest(originalRaw), Receipt: originalRaw, Outcome: "adopted"}
	raw, _ := json.Marshal(r)
	if _, err := DecodeBackupResolutionReceipt(raw); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{strings.Replace(string(raw), `"original_operation_id":"original"`, `"original_operation_id":"unrelated"`, 1), strings.Replace(string(raw), `"original_kind":"native_backup_activate"`, `"original_kind":"native_capture"`, 1), strings.Replace(string(raw), `"binding_id":"`+r.BindingID, `"binding_id":"`+strings.Repeat("9", 32), 1), strings.Replace(string(raw), `"kind":`, `"force":true,"kind":`, 1)} {
		if _, err := DecodeBackupResolutionReceipt([]byte(invalid)); err == nil {
			t.Fatal("crossed or raw resolution receipt admitted")
		}
	}
}
