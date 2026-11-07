//go:build linux

package enroll

import (
	"context"
	"os"

	"github.com/ShaulLavo/brine/internal/restic"
	"github.com/ShaulLavo/brine/internal/runnerstorage"
	"github.com/ShaulLavo/brine/internal/workspaceobserve"
)

type enrollmentArtifactID uint8

const (
	artifactBinary enrollmentArtifactID = iota
	artifactObserverUnit
	artifactReconcileUnit
	artifactReconcileGenerator
	artifactPolkit
	artifactInventoryKey
	artifactRequester
	artifactDeployKey
	artifactSSHPolicy
	artifactCaddyImport
	artifactOperatorPolicy
	artifactObserverGrant
	artifactLitestream
	artifactRestic
	artifactNativeReader
	artifactRunnerStorage
	artifactAdmissionState
	artifactAdmissionLock
	artifactAdmissionStageLock
	artifactAdmissionSettle
	artifactCount
)

type enrollmentArtifactClass string

const (
	artifactAdmission     enrollmentArtifactClass = "admission"
	artifactUpdatable     enrollmentArtifactClass = "updatable"
	artifactAuthority     enrollmentArtifactClass = "authority"
	artifactOperatorOwned enrollmentArtifactClass = "operator-owned"
	artifactToolPin       enrollmentArtifactClass = "tool/data-pin"
)

type enrollmentArtifact struct {
	path  string
	mode  os.FileMode
	class enrollmentArtifactClass
	// A nil renderer leaves bytes to the existing installer, not a host read.
	// Operator-owned renderers supply enrollment defaults only.
	render func(*host) []byte
}

func enrollmentArtifacts() [artifactCount]enrollmentArtifact {
	return [artifactCount]enrollmentArtifact{
		artifactAdmissionState:     {admissionDir + "/state.json", 0644, artifactAdmission, func(*host) []byte { return []byte(`{"version":1,"open":true}`) }},
		artifactAdmissionLock:      {admissionDir + "/lock", 0644, artifactAdmission, func(*host) []byte { return nil }},
		artifactAdmissionStageLock: {admissionDir + "/stage.lock", 0644, artifactAdmission, func(*host) []byte { return nil }},
		artifactAdmissionSettle:    {settleUnitPath, 0644, artifactUpdatable, func(*host) []byte { return []byte(settleUnit) }},
		artifactBinary:             {binaryPath, 0755, artifactUpdatable, func(h *host) []byte { return h.source }},
		artifactObserverUnit:       {workspaceobserve.UnitPath, 0644, artifactUpdatable, func(*host) []byte { return []byte(workspaceobserve.Unit) }},
		artifactReconcileUnit:      {reconcileUnitPath, 0644, artifactUpdatable, func(*host) []byte { return []byte(reconcileUnit) }},
		artifactReconcileGenerator: {reconcileGeneratorPath, 0755, artifactUpdatable, func(*host) []byte { return []byte(reconcileGenerator) }},
		artifactPolkit:             {rulePath, 0644, artifactAuthority, func(*host) []byte { return []byte(policy) }},
		artifactInventoryKey:       {sshIdentityPath, 0644, artifactAuthority, func(h *host) []byte { return h.identityKey }},
		artifactRequester: {requesterPath, 0644, artifactAuthority, func(h *host) []byte {
			return []byte("deploy:" + hash([]byte(h.r.Key)) + "\n")
		}},
		artifactDeployKey:      {sshKeyPath, 0644, artifactAuthority, func(h *host) []byte { return h.keyFile() }},
		artifactSSHPolicy:      {sshPolicyPath, 0644, artifactAuthority, func(*host) []byte { return []byte(sshPolicy) }},
		artifactCaddyImport:    {mainPath, 0644, artifactAuthority, nil},
		artifactOperatorPolicy: {operatorPolicyPath, 0644, artifactOperatorOwned, nil},
		artifactObserverGrant: {workspaceobserve.GrantPath, 0600, artifactOperatorOwned, func(*host) []byte {
			return []byte(workspaceobserve.DefaultGrant)
		}},
		artifactLitestream:    {LitestreamPath, 0755, artifactToolPin, nil},
		artifactRestic:        {restic.ToolPath, 0755, artifactToolPin, nil},
		artifactNativeReader:  {NativeReaderPinPath, 0644, artifactToolPin, nil},
		artifactRunnerStorage: {runnerstorage.Path, 0644, artifactToolPin, nil},
	}
}

func (a enrollmentArtifact) step(h *host, name string) Step {
	return Step{Name: name, Check: func(context.Context) (bool, error) {
		return h.fileMatches(a.path, hash(a.render(h)))
	}, Apply: func(ctx context.Context) error {
		return h.file(ctx, a.path, a.render(h), a.mode, false)
	}, Undo: func(context.Context) error { return h.restoreFile(a.path) }}
}
