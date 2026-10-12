package plan

import (
	"slices"
	"strconv"
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
		binding := SecretBinding{Environment: secret.Name, Reference: secret.Reference}
		prefix := "brine." + string(in.Desired.Name) + "." + string(secret.Reference) + ".v"
		var latest uint64
		if current != nil && current.Secrets.Status == target.KnownStatus && current.Secrets.Value != nil {
			binding, latest = managedFrontendSecret(*current.Secrets.Value, prefix, binding)
		}
		if latest == 0 {
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

func managedFrontendSecret(secrets []target.Secret, prefix string, binding SecretBinding) (SecretBinding, uint64) {
	var latest uint64
	for _, secret := range secrets {
		if !strings.HasPrefix(secret.Name, prefix) {
			continue
		}
		version, err := strconv.ParseUint(strings.TrimPrefix(secret.Name, prefix), 10, 64)
		if err != nil || version <= latest || secret.Name != prefix+strconv.FormatUint(version, 10) {
			continue
		}
		latest = version
		binding.VersionName = secret.Name
		binding.ID = secret.ID
	}
	return binding, latest
}
