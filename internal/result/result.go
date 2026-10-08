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
	InternalError       Code = "internal_error"
	InvalidUsage        Code = "invalid_usage"
	DependencyMissing   Code = "dependency_missing"
	PolicyRefused       Code = "policy_refused"
	Conflict            Code = "conflict"
	RecoveryRequired    Code = "recovery_required"
	Interrupted         Code = "interrupted"
	TUIInteractive      Code = "tui_interactive"
	TUITerminalRequired Code = "tui_terminal_required"
	InputRequired       Code = "input_required"
)

type description struct {
	category  Category
	message   string
	retryable bool
}

var descriptions = map[Code]description{
	InternalError:       {Operational, "The operation failed.", false},
	InvalidUsage:        {Validation, "Invalid command or arguments. Use --help for usage.", false},
	DependencyMissing:   {Dependency, "A required dependency is missing or incompatible.", false},
	PolicyRefused:       {Policy, "The operation was refused by policy.", false},
	Conflict:            {StateConflict, "The plan is stale or another operation holds the lock.", true},
	RecoveryRequired:    {Recovery, "Manual recovery is required before continuing.", false},
	Interrupted:         {Interruption, "The client was interrupted.", false},
	TUIInteractive:      {Validation, "tui is interactive; remove --json, --jsonl and --no-input", false},
	TUITerminalRequired: {Validation, "tui requires terminal input and output.", false},
	InputRequired:       {Validation, "Interactive input is required; supply explicit arguments or use an interactive terminal.", false},
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
