// Package result defines the machine response and safe domain error contract.
package result

import (
	"context"
	"errors"
)

const SchemaVersion = 1

type Category int

const (
	Operational   Category = 1
	Validation    Category = 2
	Dependency    Category = 3
	Policy        Category = 4
	StateConflict Category = 5
	Recovery      Category = 6
	Interruption  Category = 130
)

type Code string

const (
	PersistentArchiveRequired Code = "persistent_archive_required"
	AppNotFound               Code = "app_not_found"
	ReleaseNotFound           Code = "release_not_found"
	RollbackNoOp              Code = "rollback_no_op"
	LogsOwnershipRefused      Code = "logs_ownership_refused"
	LogsTruncated             Code = "logs_truncated"
	LogsLimitExceeded         Code = "logs_limit_exceeded"
	LogsInvalidJournal        Code = "logs_invalid_journal"
	LogsInventoryFailed       Code = "logs_inventory_failed"
	LogsInventoryTimeout      Code = "logs_inventory_timeout"
	LogsJournalFailed         Code = "logs_journal_failed"
	LogsJournalTimeout        Code = "logs_journal_timeout"
	LogsJournalUnavailable    Code = "logs_journal_unavailable"
	LogsContainerFailed       Code = "logs_container_failed"
	LogsContainerTimeout      Code = "logs_container_timeout"
	LogsContainerUnavailable  Code = "logs_container_unavailable"
	LogsInvalidContainer      Code = "logs_invalid_container"
	InternalError             Code = "internal_error"
	InvalidUsage              Code = "invalid_usage"
	DependencyMissing         Code = "dependency_missing"
	PolicyRefused             Code = "policy_refused"
	Conflict                  Code = "conflict"
	RecoveryRequired          Code = "recovery_required"
	Interrupted               Code = "interrupted"
	TUIInteractive            Code = "tui_interactive"
	TUITerminalRequired       Code = "tui_terminal_required"
	InputRequired             Code = "input_required"
	OfflineRequired           Code = "offline_required"
	DispatchInvalidRequest    Code = "dispatch_invalid_request"
	DispatchUnsupportedSchema Code = "dispatch_unsupported_schema"
	DispatchOperationRefused  Code = "dispatch_operation_refused"
	DispatchRootRefused       Code = "dispatch_root_refused"
	TransportInvalidTarget    Code = "transport_invalid_target"
	TransportFailure          Code = "transport_failure"
	TransportInvalidResponse  Code = "transport_invalid_response"
)

const BackupAdmissionRefreshRequired Code = "backup_admission_refresh_required"

type description struct {
	category  Category
	message   string
	retryable bool
}

var descriptions = map[Code]description{
	BackupAdmissionRefreshRequired: {Policy, "Backup credential delivery refused: the operator policy changed. An explicit approved admission refresh is required before continuing.", false},
	PersistentArchiveRequired:      {Policy, "Removal refused. Persistent data needs the P04-08 archive path; no data will be deleted.", false},
	AppNotFound:                    {Validation, "The app has no committed Brine release.", false},
	ReleaseNotFound:                {Validation, "The rollback release is not known for this app.", false},
	RollbackNoOp:                   {StateConflict, "The rollback target is already current or requires no changes.", false},
	LogsTruncated:                  {Operational, "The log output was truncated; request a smaller tail.", false},
	LogsOwnershipRefused:           {Policy, "Logs are readable only for an observed Brine-owned app unit.", false},
	LogsLimitExceeded:              {Operational, "The log response exceeds its bounded tail or byte limit; request a smaller tail.", false},
	LogsInvalidJournal:             {Operational, "The journal response is malformed.", false},
	LogsInventoryFailed:            {Operational, "Log collection failed while checking app ownership in host inventory.", false},
	LogsInventoryTimeout:           {Operational, "Log collection timed out while checking app ownership in host inventory.", false},
	LogsJournalFailed:              {Operational, "Log collection failed while running journalctl as the enrolled runner; inspect its journal access and command exit.", false},
	LogsJournalTimeout:             {Operational, "Log collection timed out while running journalctl as the enrolled runner.", false},
	LogsJournalUnavailable:         {Dependency, "Unit journal logs require journalctl and readable journals. Use app logs for container output; ask the operator to inspect unit logs without widening deploy credentials.", false},
	LogsContainerFailed:            {Operational, "Log collection failed while inspecting or reading the owned app container with Podman.", false},
	LogsContainerTimeout:           {Operational, "Log collection timed out while inspecting or reading the owned app container with Podman.", false},
	LogsContainerUnavailable:       {Dependency, "App logs require Podman and the managed k8s-file log driver; inspect the app unit and container configuration.", false},
	LogsInvalidContainer:           {Operational, "The container log response is malformed.", false},
	DispatchInvalidRequest:         {Validation, "The dispatcher request is invalid.", false},
	DispatchUnsupportedSchema:      {Dependency, "The dispatcher protocol version is incompatible.", false},
	DispatchOperationRefused:       {Policy, "The dispatcher operation is not allowed.", false},
	DispatchRootRefused:            {Policy, "The host dispatcher cannot run as root.", false},
	TransportInvalidTarget:         {Validation, "The SSH target configuration is invalid.", false},
	TransportFailure:               {Operational, "The SSH transport failed; reconcile before retrying any mutation.", false},
	TransportInvalidResponse:       {Operational, "The SSH dispatcher response is invalid.", false},
	OfflineRequired:                {Validation, "Choose --target NAME for connected planning, or use --offline with --snapshot and --policy.", false},
	InternalError:                  {Operational, "The operation failed.", false},
	InvalidUsage:                   {Validation, "Invalid command or arguments. Use --help for usage.", false},
	DependencyMissing:              {Dependency, "A required dependency is missing or incompatible.", false},
	PolicyRefused:                  {Policy, "The operation was refused by policy.", false},
	Conflict:                       {StateConflict, "The plan is stale or another operation holds the lock.", true},
	RecoveryRequired:               {Recovery, "Manual recovery is required before continuing.", false},
	Interrupted:                    {Interruption, "The client was interrupted.", false},
	TUIInteractive:                 {Validation, "tui is interactive; remove --json, --jsonl and --no-input", false},
	TUITerminalRequired:            {Validation, "tui requires terminal input and output.", false},
	InputRequired:                  {Validation, "Interactive input is required; supply explicit arguments or use an interactive terminal.", false},
}

// Error keeps the cause for reconciliation without exposing it in presentations.
// Codes select fixed messages; external values never become message text.
type Error struct {
	code  Code
	cause error
}

func New(code Code, cause error) *Error {
	if _, ok := descriptions[code]; !ok {
		code = InternalError
	}
	return &Error{code: code, cause: cause}
}

func (e *Error) Error() string      { return descriptions[e.Code()].message }
func (e *Error) Unwrap() error      { return e.cause }
func (e *Error) Category() Category { return descriptions[e.Code()].category }
func (e *Error) Code() Code {
	if _, ok := descriptions[e.code]; !ok {
		return InternalError
	}
	return e.code
}

func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return New(Interrupted, err)
	}
	var domain *Error
	if errors.As(err, &domain) {
		return domain
	}
	return New(InternalError, err)
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	return int(Classify(err).Category())
}

type MachineError struct {
	Code      Code   `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Envelope struct {
	SchemaVersion int           `json:"schema_version"`
	Command       string        `json:"command"`
	OK            bool          `json:"ok"`
	Data          any           `json:"data"`
	Error         *MachineError `json:"error"`
}

func Success(command string, data any) Envelope {
	return Envelope{SchemaVersion: SchemaVersion, Command: command, OK: true, Data: data}
}

func Failure(command string, err error) Envelope {
	domain := Classify(err)
	d := descriptions[domain.Code()]
	return Envelope{SchemaVersion: SchemaVersion, Command: command, Error: &MachineError{domain.Code(), d.message, d.retryable}}
}

// KnownCode reports whether a machine error belongs to this result schema.
func KnownCode(code Code) bool { _, ok := descriptions[code]; return ok }
