package enroll

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ShaulLavo/brine/internal/target"
	"sort"
)

// Bind only enrollment-relevant observations, not volatile disk free space or
// unrelated application probes. The fixed change list and exact apt transaction
// are included; the host repeats this comparison before changing anything.
func confirmationBinding(f Facts) (string, error) {
	plan, err := MakePlan(f)
	if err != nil {
		return "", err
	}
	ports := []target.Port{}
	owners := []target.PortOwner{}
	for _, port := range *f.Snapshot.UsedPorts.Value {
		if port == 80 || port == 443 {
			ports = append(ports, port)
		}
	}
	for _, owner := range *f.Snapshot.PortOwners.Value {
		if owner.Port == 80 || owner.Port == 443 {
			owners = append(owners, target.PortOwner{Port: owner.Port, Process: owner.Process})
		}
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i] < ports[j] })
	sort.Slice(owners, func(i, j int) bool {
		if owners[i].Port != owners[j].Port {
			return owners[i].Port < owners[j].Port
		}
		return owners[i].Process < owners[j].Process
	})
	data, err := json.Marshal(struct {
		Plan                                                Plan
		HostKey                                             string
		Packages                                            map[string]string
		Install                                             []Package
		OS                                                  target.OS
		Arch                                                string
		Versions                                            target.Versions
		Runner                                              target.Observation[string]
		Owned                                               bool
		Ports                                               []target.Port
		Owners                                              []target.PortOwner
		Environment                                         string
		PAMChecked, PAMEnvironment, SSHAuthorizationChecked bool
	}{plan, f.HostKey, f.Packages, f.PackageInstall, f.Snapshot.OS, f.Snapshot.Arch, f.Snapshot.Versions, f.Snapshot.Runner.User, f.OwnedRunner, ports, owners, f.PermitUserEnvironment, f.PAMChecked, f.PAMUserEnvironment, f.SSHAuthorizationChecked})
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
