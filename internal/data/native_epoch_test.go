package data

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestNativeEpochDerivesStableIdentityAndOwnsEvidence(t *testing.T) {
	transition := transitionFixture(t)
	epoch, err := NewNativeEpoch(transition, strings.Repeat("1", 64), strings.Repeat("2", 64), time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	if err != nil || !epoch.Matches(transition) {
		t.Fatal(err)
	}
	decoded, err := DecodeNativeEpoch(epoch.Bytes())
	if err != nil || decoded.SHA256() != epoch.SHA256() || !bytes.Equal(decoded.Bytes(), epoch.Bytes()) {
		t.Fatal("native reservation round trip drifted", err)
	}
	returned := epoch.Record()
	returned.EpochID = transition.Common().Base.epoch()
	returnedBytes := epoch.Bytes()
	returnedBytes[0] = 'x'
	if epoch.Bytes()[0] != '{' || epoch.Record().EpochID == returned.EpochID {
		t.Fatal("caller changed reservation evidence")
	}
	common := transition.Common()
	common.Scope.ID = strings.Repeat("1", 32)
	other, err := NewEnginePublicationTransition(common, EnginePublicationTarget{CandidateSHA256: strings.Repeat("8", 64), Receipt: transitionEvidence(common.Scope, common.NewEpochID, EnginePublicationEvidence, 8)})
	if err != nil {
		t.Fatal(err)
	}
	if epoch.Matches(other) {
		t.Fatal("reservation consumed by another transition")
	}
}

func TestNativeEpochCodecRejectsForeignNullAndUnknownReservations(t *testing.T) {
	transition := transitionFixture(t)
	epoch, err := NewNativeEpoch(transition, strings.Repeat("1", 64), strings.Repeat("2", 64), time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	valid := string(epoch.Bytes())
	for _, raw := range []string{
		strings.Replace(valid, `"engine":"spacetimedb"`, `"engine":"sqlite"`, 1),
		strings.Replace(valid, `"repository_id":`, `"future":true,"repository_id":`, 1),
		strings.Replace(valid, `"repository_id":`, `"Repository_id":`, 1),
		strings.Replace(valid, `"engine":"spacetimedb"`, `"engine":null`, 1),
		strings.Replace(valid, `"reservation":`, `"reservation":null,"unused":`, 1),
		strings.Replace(valid, `"epoch_id":`, `"epoch_id":null,"duplicate":`, 1),
		valid + `{}`,
	} {
		if _, err := DecodeNativeEpoch([]byte(raw)); err == nil {
			t.Fatal("invalid native epoch accepted")
		}
	}
}
