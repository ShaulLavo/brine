//go:build linux

package host

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type moduleStageStepError struct {
	code  string
	cause error
}

func (e moduleStageStepError) Error() string { return "private native stage refused" }
func (e moduleStageStepError) Unwrap() error {
	if e.cause != nil {
		return e.cause
	}
	return datapublish.ErrRecord
}

func moduleStageDiagnosticCode(code string) bool {
	return datapublish.GuestStageFailureCode(code)
}

func emitModuleStageDiagnostic(output io.Writer, code string) error {
	if output == nil || !moduleStageDiagnosticCode(code) {
		return datapublish.ErrRecord
	}
	return json.NewEncoder(output).Encode(struct {
		Format string `json:"format"`
		Code   string `json:"code"`
	}{"brine-module-stage-refusal-v1", code})
}

func decodeModuleStageDiagnostic(raw []byte) string {
	if len(raw) == 0 || len(raw) > 1024 {
		return ""
	}
	var frame struct {
		Format string `json:"format"`
		Code   string `json:"code"`
	}
	if _, err := strictjson.Object(raw, "format", "code"); err != nil {
		return ""
	}
	if json.Unmarshal(raw, &frame) != nil || frame.Format != "brine-module-stage-refusal-v1" || !moduleStageDiagnosticCode(frame.Code) {
		return ""
	}
	return frame.Code
}

func moduleStageFailureCode(err error, fallback string) string {
	var step moduleStageStepError
	if errors.As(err, &step) && moduleStageDiagnosticCode(step.code) {
		return step.code
	}
	return fallback
}

func moduleStageTerminalCode(primary string, observeErr, stopErr error) string {
	if observeErr == nil && stopErr != nil {
		return "guest_child_stop"
	}
	return primary
}

func moduleStageRunFailure(err error, output []byte) error {
	if code := decodeModuleStageDiagnostic(output); code != "" {
		return errors.Join(err, moduleStageStepError{code: code})
	}
	return err
}
