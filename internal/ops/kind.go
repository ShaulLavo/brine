package ops

import (
	"encoding/json"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"regexp"
	"strconv"
	"strings"
)

type Kind string

const (
	Deploy                    Kind = "deploy"
	Resolve                   Kind = "resolve"
	SecretSet                 Kind = "secret_set"
	NativeMaterialSet         Kind = "native_material_set"
	Reconcile                 Kind = "reconcile"
	RestoreTest               Kind = "restore_test"
	CredentialActivation      Kind = "credential_activation" // #nosec G101 -- A closed operation name, not credential material.
	DataInitApply             Kind = "data_init_apply"
	NativeCapture             Kind = "native_capture"
	NativeRestoreTest         Kind = "native_restore_test"
	NativePrepare             Kind = "native_prepare"
	NativeRecoverySet         Kind = "native_recovery_set"
	NativeBackupActivate      Kind = "native_backup_activate"
	NativeBackupResolve       Kind = "native_backup_resolve"
	ModulePublishApply        Kind = "module_publish_apply"
	ModulePublishResolve      Kind = "module_publish_resolve"
	NativeLiveRestore         Kind = "native_live_restore"
	NativeLiveRestoreResolve  Kind = "native_live_restore_resolve"
	NativeRetire              Kind = "native_retire"
	NativeRetireResolve       Kind = "native_retire_resolve"
	ArchiveMaintenanceApply   Kind = "archive_maintenance_apply"
	ArchiveMaintenanceResolve Kind = "archive_maintenance_resolve"
)

// Intent contains identity and references only. Secret values cannot be stored.
type Intent struct {
	Kind       Kind
	RecoveryOf string
	PlanID     string
	App        string
	SecretRef  string
}

var operationID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)

var appName = regexp.MustCompile(`^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var secretRef = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,252}$`)
var planID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ValidIntent(i Intent) bool {
	if !i.Kind.IsResolver() && i.RecoveryOf != "" {
		return false
	}
	switch i.Kind {
	case ModulePublishApply, NativeLiveRestore, NativeRetire, ArchiveMaintenanceApply:
		return i.PlanID == "" && appName.MatchString(i.App) && planID.MatchString(i.SecretRef)
	case NativeBackupResolve:
		return i.PlanID == "" && appName.MatchString(i.App) && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(i.SecretRef) && operationID.MatchString(i.RecoveryOf)
	case ModulePublishResolve, NativeLiveRestoreResolve, NativeRetireResolve, ArchiveMaintenanceResolve:
		return i.PlanID == "" && appName.MatchString(i.App) && planID.MatchString(i.SecretRef) && operationID.MatchString(i.RecoveryOf)
	case Resolve:
		return operationID.MatchString(i.RecoveryOf) && (planID.MatchString(i.PlanID) && i.App == "" && i.SecretRef == "" || i.PlanID == "" && ValidIntent(Intent{Kind: SecretSet, App: i.App, SecretRef: i.SecretRef}))
	case Deploy:
		return planID.MatchString(i.PlanID) && i.App == "" && i.SecretRef == ""
	case RestoreTest, CredentialActivation, DataInitApply, NativeCapture, NativeRestoreTest, NativePrepare, NativeRecoverySet, NativeBackupActivate:
		return i.PlanID == "" && appName.MatchString(i.App) && (operationID.MatchString(i.SecretRef) || planID.MatchString(i.SecretRef))
	case Reconcile:
		return i.PlanID == "" && i.App == "" && i.SecretRef == ""
	case NativeMaterialSet:
		return i.PlanID == "" && appName.MatchString(i.App) && planID.MatchString(i.SecretRef)
	case SecretSet:
		return i.PlanID == "" && appName.MatchString(i.App) && secretRef.MatchString(i.SecretRef) && len("brine."+i.App+"."+i.SecretRef+".v18446744073709551615") <= 253
	default:
		return false
	}
}
func ValidOperation(o Operation) bool {
	if !o.Kind.IsResolver() && o.RecoveryOf != "" {
		return false
	}
	if !ValidState(o.State) {
		return false
	}
	if o.Kind == Reconcile || o.Kind.IsTask() {
		return ValidIntent(Intent{Kind: o.Kind, PlanID: o.PlanID, App: o.App, SecretRef: o.SecretRef, RecoveryOf: o.RecoveryOf}) && (o.State == Queued || o.State == LaunchUnknown || o.State == Preflight || o.State == Succeeded || o.State == Failed || o.State == RecoveryRequired)
	}
	if o.Kind == Resolve {
		return (o.PlanID != "" && ValidIntent(Intent{Kind: Resolve, PlanID: o.PlanID, RecoveryOf: o.RecoveryOf}) && appName.MatchString(o.App) && o.SecretRef == "") || (o.PlanID == "" && ValidIntent(Intent{Kind: Resolve, App: o.App, SecretRef: o.SecretRef, RecoveryOf: o.RecoveryOf}))
	}
	if o.Kind == Deploy {
		return ValidIntent(Intent{Kind: o.Kind, PlanID: o.PlanID}) && o.SecretRef == "" && (o.App == "" || appName.MatchString(o.App))
	}
	if !ValidIntent(Intent{Kind: o.Kind, PlanID: o.PlanID, App: o.App, SecretRef: o.SecretRef}) {
		return false
	}
	return o.State == Queued || o.State == Preparing || o.State == Succeeded || o.State == Failed || o.State == RecoveryRequired
}
func TransitionsFor(kind Kind) map[State][]State {
	switch kind {
	case Deploy, Resolve:
		return Transitions()
	case NativeBackupResolve, Reconcile, RestoreTest, CredentialActivation, DataInitApply, NativeCapture, NativeRestoreTest, NativePrepare, NativeRecoverySet, NativeBackupActivate, ModulePublishApply, ModulePublishResolve, NativeLiveRestore, NativeLiveRestoreResolve, NativeRetire, NativeRetireResolve, ArchiveMaintenanceApply, ArchiveMaintenanceResolve:
		return map[State][]State{Queued: {LaunchUnknown, Preflight, Failed, RecoveryRequired}, LaunchUnknown: {Preflight, Failed, RecoveryRequired}, Preflight: {Succeeded, Failed, RecoveryRequired}}
	case SecretSet, NativeMaterialSet:
		return map[State][]State{Queued: {Preparing}, Preparing: {Succeeded, Failed, RecoveryRequired}}
	default:
		return map[State][]State{}
	}
}
func CanTransitionFor(kind Kind, from, to State) bool {
	for _, s := range TransitionsFor(kind)[from] {
		if s == to {
			return true
		}
	}
	return false
}

type SecretVersionPayload struct {
	Name    string `json:"name"`
	Outcome string `json:"outcome"`
}

func ValidSecretVersionName(name string) bool {
	parts := strings.Split(name, ".")
	if len(name) > 253 || len(parts) != 4 || parts[0] != "brine" || !appName.MatchString(parts[1]) || !secretRef.MatchString(parts[2]) || !strings.HasPrefix(parts[3], "v") {
		return false
	}
	version := strings.TrimPrefix(parts[3], "v")
	n, err := strconv.ParseUint(version, 10, 64)
	return err == nil && n > 0 && version == strconv.FormatUint(n, 10)
}

func ValidateOperationEvent(op Operation, e Event) error {
	if ValidateEvent(e) != nil {
		return ErrInvalidEvent
	}
	if op.Kind == Deploy || op.Kind == Resolve {
		if e.Kind == "resolution" {
			var p ResolutionPayload
			if op.Kind != Resolve || json.Unmarshal(e.Payload, &p) != nil || p.OperationID != op.RecoveryOf {
				return ErrInvalidEvent
			}
			return nil
		}
		if op.Kind == Resolve && op.PlanID == "" && e.Kind == "step" {
			return ErrInvalidEvent
		}
		if e.Kind == "secret_version" {
			if op.Kind != Resolve || op.PlanID != "" {
				return ErrInvalidEvent
			}
			var p SecretVersionPayload
			if json.Unmarshal(e.Payload, &p) != nil || !strings.HasPrefix(p.Name, "brine."+op.App+"."+op.SecretRef+".v") {
				return ErrInvalidEvent
			}
		}
		return nil
	}
	if op.Kind == Reconcile || op.Kind.IsTask() {
		if e.Kind != "launch" && e.Kind != "state" && e.Kind != "failure" {
			return ErrInvalidEvent
		}
		return nil
	}
	if op.Kind == NativeMaterialSet {
		if e.Kind != "state" && e.Kind != "failure" && e.Kind != "native_material" {
			return ErrInvalidEvent
		}
		if e.Kind == "native_material" {
			receipt, err := nativematerial.DecodeReceipt(e.Payload)
			if err != nil || receipt.OperationID != op.ID || receipt.App != op.App || receipt.PreparationPlanID != op.SecretRef || receipt.Outcome == "recovery_required" {
				return ErrInvalidEvent
			}
		}
		return nil
	}
	if op.Kind != SecretSet || e.Kind != "secret_version" && e.Kind != "state" && e.Kind != "failure" {
		return ErrInvalidEvent
	}
	if e.Kind == "secret_version" {
		var p SecretVersionPayload
		if json.Unmarshal(e.Payload, &p) != nil || !strings.HasPrefix(p.Name, "brine."+op.App+"."+op.SecretRef+".v") {
			return ErrInvalidEvent
		}
	}
	return nil
}

// IsTask identifies reference-only detached work; handlers own their host locking.
func (k Kind) IsTask() bool {
	return k == ArchiveMaintenanceApply || k == ArchiveMaintenanceResolve || k == NativeBackupResolve || k == RestoreTest || k == CredentialActivation || k == DataInitApply || k == NativeCapture || k == NativeRestoreTest || k == NativePrepare || k == NativeRecoverySet || k == NativeBackupActivate || k == ModulePublishApply || k == ModulePublishResolve || k == NativeLiveRestore || k == NativeLiveRestoreResolve || k == NativeRetire || k == NativeRetireResolve
}

func (k Kind) IsResolver() bool {
	switch k {
	case Resolve, ModulePublishResolve, NativeLiveRestoreResolve, NativeRetireResolve, NativeBackupResolve, ArchiveMaintenanceResolve:
		return true
	default:
		return false
	}
}
