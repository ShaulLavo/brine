//go:build linux

package host

import (
	"encoding/json"
	"errors"
	"io"

	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

type moduleStageStepError struct{ code string }

func (e moduleStageStepError) Error() string { return "private native stage refused" }
func (e moduleStageStepError) Unwrap() error { return datapublish.ErrRecord }

func moduleStageDiagnosticCode(code string) bool {
	switch code {
	case "guest_containment", "guest_input", "guest_source", "guest_credential", "guest_child_start", "guest_child_bind", "guest_listener", "guest_publisher", "guest_operation", "guest_child_stop", "guest_output":
		return true
	default:
		return false
	}
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

func moduleStageRunFailure(err error, output []byte) error {
	if code := decodeModuleStageDiagnostic(output); code != "" {
		return errors.Join(err, moduleStageStepError{code: code})
	}
	return err
}
