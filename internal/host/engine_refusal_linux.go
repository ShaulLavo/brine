//go:build linux

package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/localexec"
	"golang.org/x/sys/unix"
)

type engineRefusalCode string

const (
	engineRefusalUnknown          engineRefusalCode = "unclassified"
	engineRefusalPermitRead       engineRefusalCode = "permit_read"
	engineRefusalAdmission        engineRefusalCode = "verification_admission"
	engineRefusalLaunchGuard      engineRefusalCode = "launch_guard"
	engineRefusalProofGuard       engineRefusalCode = "proof_guard"
	engineRefusalUnit             engineRefusalCode = "protected_unit"
	engineRefusalProcess          engineRefusalCode = "supervisor_identity"
	engineRefusalManager          engineRefusalCode = "manager_start_invocation"
	engineRefusalContainers       engineRefusalCode = "container_census"
	engineRefusalSourceLive       engineRefusalCode = "source_activity"
	engineRefusalAllocation       engineRefusalCode = "source_allocation"
	engineRefusalMaterial         engineRefusalCode = "protected_material"
	engineRefusalNonce            engineRefusalCode = "verification_nonce"
	engineRefusalCopy             engineRefusalCode = "source_copy"
	engineRefusalPrivateConfig    engineRefusalCode = "private_module_http"
	engineRefusalPrivateBootstrap engineRefusalCode = "private_bootstrap_removal"
	engineRefusalPrivateArgv      engineRefusalCode = "private_arguments"
	engineRefusalPrivateInput     engineRefusalCode = "private_input"
	engineRefusalPrivateRun       engineRefusalCode = "private_process_failed"
	engineRefusalPrivateTimeout   engineRefusalCode = "private_process_timeout"
	engineRefusalPrivateUnknown   engineRefusalCode = "private_process_unknown"
	engineRefusalPrivateDisposal  engineRefusalCode = "private_disposal_unknown"
	engineRefusalPrivateQuota     engineRefusalCode = "private_output_quota"
	engineRefusalPrivateDecode    engineRefusalCode = "private_observation_decode"
	engineRefusalPrivateMismatch  engineRefusalCode = "private_observation_mismatch"
	engineRefusalSourceChanged    engineRefusalCode = "source_changed"
	engineRefusalStageDisposal    engineRefusalCode = "private_stage_disposal"
	engineRefusalPermitChanged    engineRefusalCode = "permit_changed"
	engineRefusalNativeStart      engineRefusalCode = "native_process_start"
	engineRefusalStoppedRemoval   engineRefusalCode = "stopped_container_removal"
	engineRefusalNativeArguments  engineRefusalCode = "native_arguments"
	engineRefusalNativeHandoff    engineRefusalCode = "native_process_handoff"
	engineRefusalNativeGuard      engineRefusalCode = "native_handoff_liveness"
	engineRefusalGuardRelease     engineRefusalCode = "launch_guard_release"
	engineRefusalNotify           engineRefusalCode = "manager_ready_notification"
	engineRefusalNativeStop       engineRefusalCode = "native_stop"
	engineRefusalNativeWait       engineRefusalCode = "native_stop_wait"
	engineRefusalNativeAbsence    engineRefusalCode = "native_absence"
	engineRefusalNativeExit       engineRefusalCode = "native_process_exit"
)

type engineRefusalError struct {
	code  engineRefusalCode
	cause error
}

func (e *engineRefusalError) Error() string { return errEngineSource.Error() }
func (e *engineRefusalError) Unwrap() error { return e.cause }

func engineRefusal(code engineRefusalCode, cause error) error {
	if cause == nil {
		cause = errEngineSource
	}
	return &engineRefusalError{code: code, cause: cause}
}

func enginePrivateRunCode(err error) engineRefusalCode {
	var failure *localexec.Error
	if !errors.As(err, &failure) {
		return engineRefusalPrivateRun
	}
	if failure.Kind == localexec.Timeout {
		return engineRefusalPrivateTimeout
	}
	if failure.Kind == localexec.UnknownOutcome {
		return engineRefusalPrivateUnknown
	}
	return engineRefusalPrivateRun
}

type engineRefusalReceipt struct {
	Version int               `json:"version"`
	Code    engineRefusalCode `json:"code"`
	At      time.Time         `json:"at"`
}

func (r *EngineStartupRuntime) recordEngineRefusal(parent context.Context, binding data.ReplicaBindingID, failure error) {
	if failure == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Second)
	defer cancel()
	if _, err := r.state.ReadReservedEngine(ctx, binding); err != nil {
		return
	}
	code := engineRefusalUnknown
	var refused *engineRefusalError
	if errors.As(failure, &refused) {
		code = refused.code
	}
	_ = writeEngineRefusal(ctx, r.stateRoot, binding, engineRefusalReceipt{Version: 1, Code: code, At: time.Now().UTC()})
}

func (r *EngineStartupRuntime) retainedEngineRefusal(ctx context.Context, binding data.ReplicaBindingID, code engineRefusalCode, cause error) error {
	failure := engineRefusal(code, cause)
	r.recordEngineRefusal(ctx, binding, failure)
	return failure
}

func writeEngineRefusal(ctx context.Context, stateRoot string, binding data.ReplicaBindingID, receipt engineRefusalReceipt) error {
	if ctx.Err() != nil || !data.ValidID(string(binding)) || !receipt.Code.valid() || receipt.Version != 1 || receipt.At.IsZero() {
		return errEngineSource
	}
	if _, err := data.InspectRoot(stateRoot); err != nil {
		return errEngineSource
	}
	root, err := engineOpenRoot(stateRoot)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	if err = engineMakePrivateDirectories(root, "engine-refusals"); err != nil {
		return err
	}
	parent, err := engineOpenRoot(filepath.Join(stateRoot, "engine-refusals"))
	if err != nil {
		return err
	}
	defer func() { _ = parent.Close() }()
	name := string(binding) + ".json"
	if err = engineRefusalTarget(parent, name); err != nil {
		return err
	}
	raw, err := json.Marshal(receipt)
	if err != nil || len(raw) > 4096 {
		return errEngineSource
	}
	id, err := data.NewID()
	if err != nil {
		return err
	}
	temporary := ".brine-" + id
	file, err := engineOpenMember(parent, temporary, unix.O_CREAT|unix.O_EXCL|unix.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close(); _ = parent.Remove(temporary) }()
	n, err := file.Write(raw)
	if err != nil || n != len(raw) || ctx.Err() != nil || file.Sync() != nil {
		return errEngineSource
	}
	if err = engineRefusalTarget(parent, name); err != nil {
		return err
	}
	if err = parent.Rename(temporary, name); err != nil {
		return err
	}
	return syncEngineDirectory(root, "engine-refusals")
}

func engineRefusalTarget(root *os.Root, name string) error {
	file, err := engineOpenMember(root, name, unix.O_RDONLY, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	member, err := engineMember(file, name)
	if err != nil || member.Mode != unix.S_IFREG|0600 || int64(member.UID) != int64(os.Geteuid()) || member.Links != 1 || member.Size > 4096 {
		return errEngineSource
	}
	return nil
}

func (c engineRefusalCode) valid() bool {
	switch c {
	case engineRefusalUnknown, engineRefusalPermitRead, engineRefusalAdmission, engineRefusalLaunchGuard, engineRefusalProofGuard, engineRefusalUnit, engineRefusalProcess, engineRefusalManager, engineRefusalContainers, engineRefusalSourceLive, engineRefusalAllocation, engineRefusalMaterial, engineRefusalNonce, engineRefusalCopy, engineRefusalPrivateConfig, engineRefusalPrivateBootstrap, engineRefusalPrivateArgv, engineRefusalPrivateInput, engineRefusalPrivateRun, engineRefusalPrivateTimeout, engineRefusalPrivateUnknown, engineRefusalPrivateDisposal, engineRefusalPrivateQuota, engineRefusalPrivateDecode, engineRefusalPrivateMismatch, engineRefusalSourceChanged, engineRefusalStageDisposal, engineRefusalPermitChanged, engineRefusalStoppedRemoval, engineRefusalNativeArguments, engineRefusalNativeStart, engineRefusalNativeHandoff, engineRefusalNativeGuard, engineRefusalGuardRelease, engineRefusalNotify, engineRefusalNativeStop, engineRefusalNativeWait, engineRefusalNativeAbsence, engineRefusalNativeExit:
		return true
	}
	return false
}
