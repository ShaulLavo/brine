package plan

import (
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/target"
)

const StopManagedFrontend ChangeKind = "stop_managed_frontend"
const RemoveManagedFrontend ChangeKind = "remove_managed_frontend"

func managedFrontend(in Input, previous *CurrentRelease, enginePort target.Port) (ManagedFrontendPlan, []Diagnostic) {
	frontend := ManagedFrontendPlan{Image: cloneManagedImage(in.Image), Secrets: []SecretBinding{}}
	conflicts := []Diagnostic{}
	add := func(code ConflictCode, field string) {
		conflicts = append(conflicts, Diagnostic{Code: code, Field: field})
	}
	if !managedImageVerified(in.Image) || in.Image.Platform.Arch != in.Snapshot.Arch || !strings.HasSuffix(string(in.Desired.Image), "@"+in.Image.Digest) {
		add(ImagePlatform, "image")
	}
	var current *target.App
	if in.Snapshot.Apps.Status == target.KnownStatus && in.Snapshot.Apps.Value != nil {
		for i := range *in.Snapshot.Apps.Value {
			if (*in.Snapshot.Apps.Value)[i].Name == string(in.Desired.Name) {
				current = &(*in.Snapshot.Apps.Value)[i]
			}
		}
	} else {
		add(UnknownFacts, "apps")
	}
	if previous != nil && previous.Managed != nil && previous.Managed.Frontend != nil {
		frontend.Port = previous.Managed.Frontend.Port
	}
	if frontend.Port == 0 {
		for _, reservation := range in.State.ManagedReservations {
			if reservation.App == string(in.Desired.Name) {
				frontend.Port = reservation.FrontendPort
			}
		}
	}
	if frontend.Port == 0 {
		ports, known := previewPorts(in)
		if !known {
			add(UnknownFacts, "ports")
		}
		for _, port := range ports {
			if port != enginePort {
				frontend.Port = port
				break
			}
		}
	}
	if frontend.Port == 0 || frontend.Port == enginePort {
		add(PortsExhausted, "managed.frontend_port")
	}
	for _, secret := range in.Desired.Secrets {
		binding, found := SecretBinding{}, false
		if current != nil && current.Secrets.Value != nil {
			binding, found = bindSecret(string(in.Desired.Name), secret, current, in.SecretExact)
		}
		if !found {
			add(SecretMissing, "secrets."+secret.Name)
		} else {
			frontend.Secrets = append(frontend.Secrets, binding)
		}
	}
	if previous != nil && previous.Managed != nil && previous.Managed.Frontend != nil {
		expected := []target.Unit{}
		for _, unit := range previous.Units {
			if unit.Name == previous.App+".container" {
				expected = append(expected, unit)
			}
		}
		if current == nil || current.QuadletUnits.Status != target.KnownStatus || current.QuadletUnits.Value == nil || !slices.Equal(*current.QuadletUnits.Value, expected) {
			add(ArtifactDrift, "managed.frontend_unit")
		}
	} else if current != nil && (current.Image.Status == target.KnownStatus || current.QuadletUnits.Status == target.KnownStatus && current.QuadletUnits.Value != nil && len(*current.QuadletUnits.Value) != 0) {
		add(ArtifactDrift, "managed.frontend_ownership")
	}
	return frontend, conflicts
}
