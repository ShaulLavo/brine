package sqliteprep

import (
	"context"
	"time"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/restore"
)

type TaskState string

const (
	Preflight        TaskState = "preflight"
	Succeeded        TaskState = "succeeded"
	Failed           TaskState = "failed"
	RecoveryRequired TaskState = "recovery_required"
)

type TaskStatus struct {
	Task    Task
	State   TaskState
	Claimed bool
	Receipt *Receipt
}

type Facts struct {
	Scope               Scope
	Fence               data.QuiescenceFence
	Location            Location
	Lineage             *Lineage
	Point               *PreparedPoint
	CredentialExpiresAt time.Time
	UncertainSnapshot   *SnapshotPlan
}

type ObjectState string

const (
	ObjectUnknown ObjectState = "unknown"
	ObjectAbsent  ObjectState = "absent"
	ObjectPresent ObjectState = "present"
)

// Journal is a protected transactional port, not a generic row writer. All calls
// rejoin the real preflight task's kind, requester, app and canonical input hash.
// Inspect derives lineage from qualified original events or immutable inherited
// claim references. CoverageRecord must prove its relationship with that exact
// baseline, not two independent same-schema rows. OwnStopped/OwnUploaded are
// derived from the original journal, never supplied by a requester.
//
// The read-only host observation and optional held source remain live during
// Claim. Its store adapter must recognize their real authority, not treat an
// arbitrary implementation as a capability.
// Claim atomically validates the current scope, terminal unconsumed predecessor,
// original held fence, observations and fixed mode; inserts its consumer, intent
// and claimed event. Append checks the canonical payload, sequence and full path.
//
// CompletePrepare checks ValidatePrepared plus all protected joins, current
// scope/policy/retention and the live stopped guard. In ONE transaction it admits
// the identical source point, inserts the prepared point/event and canonical
// receipt, and completes the task and its state event. No post-handler success
// write is permitted. CompleteAbandon similarly inserts its consumer and completion, releases ONLY the original fence and completes its
// task/receipt/state event atomically. A typed Refusal from CompleteAbandon means
// no effect was issued; FailUnclaimed still confirms no claim transactionally.
// Other completion errors remain uncertain until joined readback. Unused points AND active consumers protect
// generic fence release. The adapters must validate on reads as well as writes.
//
// ReadTask returns success only after the full joined historical completion is
// validated. Later legitimate consumption or lifecycle changes do not invalidate
// that immutable result. Current point admission is a separate protected read.
// A point-shaped row alone is not success. FailUnclaimed confirms transactionally
// that no claim exists before recording Failed. MarkRecovery never overwrites a
// committed outcome. A lost reply is inspected, never replayed. Test fakes cannot
// establish these joins or grant actual authority. U3b owns those guarantees.
type Journal interface {
	ReadTask(context.Context, Task) (TaskStatus, error)
	Inspect(context.Context, Task, Input) (Facts, error)
	Claim(context.Context, Intent, HostObservation, StoppedSource) error
	Append(context.Context, Intent, Event) error
	CompletePrepare(context.Context, History, PreparedPoint, StoppedSource) (Receipt, error)
	CompleteAbandon(context.Context, Task, Input, Abandonment, StoppedSource) (Receipt, error)
	FailUnclaimed(context.Context, Task, Code) error
	MarkRecovery(context.Context, Task, Code) error
}

// Host acquires the existing host mutation lock. The resulting guard binds the
// exact scope and immutable fence owner, and rechecks policy before effects.
// No start, restart, exec, credential activation or remote deletion port exists.
type Host interface {
	Acquire(context.Context, Scope, data.QuiescenceFence) (HostGuard, error)
}
type HostObservation interface {
	Observe(context.Context) (Observation, error)
}
type HostGuard interface {
	HostObservation
	StopWriters(context.Context, Intent) error
	Barrier(context.Context, Intent, time.Time) (RemoteSource, error)
	StopReplica(context.Context, Intent) error
	HoldStopped(context.Context) (StoppedSource, error)
	Capture(context.Context, Intent, StoppedSource, time.Time) (SnapshotPlan, error)
	// Upload calls the shipped create + expected-412 capability protocol ONCE.
	// An unknown response on either request ends the invocation without another.
	Upload(context.Context, Intent, SnapshotPlan) (RemoteSource, error)
	// Verify independently restores the exact source/checks. It returns metadata
	// only after its workspace is disposed, including cancellation/error cleanup.
	Verify(context.Context, Intent, RemoteSource) (restore.SQLiteReceipt, error)
	// OldObject is optional informational HEAD only. It never adopts or authorizes
	// a PUT, point or release and need not have available credentials.
	OldObject(context.Context, SnapshotPlan) (ObjectState, error)
	Close() error
}

// StoppedSource retains original source handles and the replica lifetime lock
// across schema observations, final rechecks AND atomic completion. Recheck must
// fail after close or loss of any held authority; the store adapter enforces it
// at the completion transaction, not by trusting a caller-supplied boolean.
type StoppedSource interface {
	Recheck(context.Context) (StoppedBundle, error)
	Schema(context.Context) (data.SQLiteSchemaObservation, error)
	Close() error
}

type Abandonment struct {
	Intent     Intent
	Point      *PreparedPoint
	Stopped    StoppedBundle
	OldObject  ObjectState
	ObservedAt time.Time
}
type Result struct {
	Receipt Receipt
	// CleanupError is diagnostic only after known atomic success. It does not
	// rewrite the stored outcome or advertise a prepared point as unusable.
	CleanupError error
}
