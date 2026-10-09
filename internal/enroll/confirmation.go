package enroll

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/target"
)

// Bind only enrollment-relevant observations, not volatile disk free space or
// unrelated application probes. The fixed change list and exact apt transaction
// are included; the host repeats this comparison before changing anything.
func confirmationBinding(f Facts) (string, error) {
	plan, err := MakePlan(f)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(struct {
		Plan                       Plan
		HostKey                    string
		Packages                   map[string]string
		Install                    []Package
		OS                         target.OS
		Arch                       string
		Versions                   target.Versions
		Runner                     target.Observation[string]
		Owned                      bool
		Ports                      target.Observation[[]target.Port]
		Owners                     target.Observation[[]target.PortOwner]
		Environment                string
		PAMChecked, PAMEnvironment bool
	}{plan, f.HostKey, f.Packages, f.PackageInstall, f.Snapshot.OS, f.Snapshot.Arch, f.Snapshot.Versions, f.Snapshot.Runner.User, f.OwnedRunner, f.Snapshot.UsedPorts, f.Snapshot.PortOwners, f.PermitUserEnvironment, f.PAMChecked, f.PAMUserEnvironment})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
func checkConfirmation(f Facts, binding string) error {
	current, err := confirmationBinding(f)
	if err != nil {
		return err
	}
	if binding == "" || binding != current {
		return errors.New("enrollment facts or confirmed package transaction changed; inventory and confirm again")
	}
	return nil
}
