package store

import (
	"reflect"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/target"
)

func verifyManagedRelease(app string, release Release, planned plan.Plan) error {
	managed := release.Managed
	expected := planned.Managed
	if expected == nil || !expected.Valid() || managed == nil || !managed.Valid() || managed.BindingID != expected.Database.BindingID || managed.EpochID != expected.Archive.EpochID || managed.EnginePort != expected.EnginePort || managed.RuntimeSHA256 != expected.RuntimeSHA256 {
		return ErrInvalid
	}
	engine := target.Unit{Name: "brine-engine-" + string(managed.BindingID) + ".service", Hash: "sha256:" + expected.EngineUnitHash}
	if managed.EngineUnit != engine || !managedUnitRecorded(release.Units, engine) {
		return ErrInvalid
	}
	if expected.Frontend == nil {
		if managed.Frontend != nil || len(release.Units) != 1 || len(release.Secrets) != 0 || !reflect.DeepEqual(release.Image, expected.EngineImage) || release.HostPort != expected.EnginePort {
			return ErrInvalid
		}
		return nil
	}
	frontend := managed.Frontend
	if frontend == nil || frontend.Unit.Name != app+".container" || !managedUnitRecorded(release.Units, frontend.Unit) || len(release.Units) != 2 || !reflect.DeepEqual(frontend.Image, expected.Frontend.Image) || frontend.Port != expected.Frontend.Port || !reflect.DeepEqual(frontend.Secrets, expected.Frontend.Secrets) || !reflect.DeepEqual(release.Image, frontend.Image) || release.HostPort != frontend.Port {
		return ErrInvalid
	}
	return nil
}

func managedUnitRecorded(units []target.Unit, wanted target.Unit) bool {
	found := false
	for _, unit := range units {
		if unit.Name == "" || !digestPattern.MatchString(unit.Hash) {
			return false
		}
		if unit.Name != wanted.Name {
			continue
		}
		if found || unit != wanted {
			return false
		}
		found = true
	}
	return found
}
