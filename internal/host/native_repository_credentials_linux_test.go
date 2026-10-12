//go:build linux

package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/store"
)

func nativeCredentialMetadataFixture(t *testing.T) (store.NativeRepositoryMaterial, backupcredentials.Receipt) {
	t.Helper()
	scope := backupcredentials.Scope{TargetHash: "sha256:" + strings.Repeat("a", 64), App: "game", CredentialRef: "backup", Destination: "backup", Binding: strings.Repeat("b", 32), Epoch: strings.Repeat("c", 32), PolicyHash: "sha256:" + strings.Repeat("d", 64)}
	expiry := time.Now().UTC().Add(time.Hour)
	plan := backupcredentials.Plan{Requester: "deploy:" + strings.Repeat("e", 64), Kind: backupcredentials.Kind, Scope: scope, Version: 1, ExpiresAt: &expiry}
	raw, _ := json.Marshal(plan)
	sum := sha256.Sum256(raw)
	plan.ID = "sha256:" + hex.EncodeToString(sum[:])
	receipt := backupcredentials.Receipt{Requester: plan.Requester, PlanID: plan.ID, Scope: scope, Version: 1, File: "s3/backup/v1.env", ReceivedAt: time.Now().UTC(), ExpiresAt: &expiry}
	if !receipt.Valid() {
		t.Fatal("invalid private receipt fixture")
	}
	record := credentialRecord(plan, "receipt")
	record.ReceivedAt = receipt.ReceivedAt
	return store.NativeRepositoryMaterial{CredentialRecord: record, CredentialReceiptID: record.ID}, receipt
}

func TestNativeRepositoryCredentialReceiptMatchesExactProtectedDelivery(t *testing.T) {
	material, receipt := nativeCredentialMetadataFixture(t)
	if !nativeCredentialReceiptMatches(material, receipt) {
		t.Fatal("exact physical delivery metadata refused")
	}
	changed := receipt
	changed.ReceivedAt = changed.ReceivedAt.Add(time.Second)
	if !changed.Valid() || nativeCredentialReceiptMatches(material, changed) {
		t.Fatal("different valid delivery stamp replaced selected metadata")
	}
	material.CredentialRecord.PlanHash = "sha256:" + strings.Repeat("f", 64)
	if nativeCredentialReceiptMatches(material, receipt) {
		t.Fatal("crossed protected delivery plan admitted")
	}
}

func TestNativeRepositoryCredentialReceiptDoesNotAcceptNewValidPlanAtSameVersion(t *testing.T) {
	material, receipt := nativeCredentialMetadataFixture(t)
	expiry := receipt.ExpiresAt.Add(time.Minute)
	replacement := backupcredentials.Plan{Requester: receipt.Requester, Kind: backupcredentials.Kind, Scope: receipt.Scope, Version: receipt.Version, ExpiresAt: &expiry}
	raw, _ := json.Marshal(replacement)
	sum := sha256.Sum256(raw)
	replacement.ID = "sha256:" + hex.EncodeToString(sum[:])
	receipt.PlanID, receipt.ExpiresAt = replacement.ID, &expiry
	if !receipt.Valid() || nativeCredentialReceiptMatches(material, receipt) {
		t.Fatal("another valid same-reference/version scope replaced selected delivery")
	}
}
