package dispatch

import (
	"bytes"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/reconcile"
	"github.com/ShaulLavo/brine/internal/restore"
	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
	"github.com/ShaulLavo/brine/internal/target"
)

func DecodeResponse(data []byte, op string) (result.Envelope, error) {
	invalid := func() (result.Envelope, error) {
		return result.Envelope{}, result.New(result.TransportInvalidResponse, nil)
	}
	if len(data) > ResponseLimit {
		return invalid()
	}
	fields, err := strictjson.Object(data, "schema_version", "command", "ok", "data", "error")
	if err != nil {
		return invalid()
	}
	version, err := strictjson.Value[int](fields["schema_version"])
	if err != nil || version != result.SchemaVersion {
		return invalid()
	}
	command, err := strictjson.Value[string](fields["command"])
	if err != nil {
		return invalid()
	}
	ok, err := strictjson.Value[bool](fields["ok"])
	if err != nil {
		return invalid()
	}
	if _, known := ClassOf(op); !known {
		return invalid()
	}
	if command != "brine host "+op && (ok || command != "brine host serve") {
		return invalid()
	}
	if ok {
		if !bytes.Equal(bytes.TrimSpace(fields["error"]), []byte("null")) {
			return invalid()
		}
		var value any
		switch op {
		case "restore_test":
			r, err := restore.DecodeReceipt(fields["data"])
			if err != nil {
				return invalid()
			}
			value = r
		case "backup_credentials_plan":
			p, err := backupcredentials.DecodePlan(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "backup_credentials_set":
			r, err := backupcredentials.DecodeReceipt(fields["data"])
			if err != nil {
				return invalid()
			}
			value = r
		case "config_set", "lifecycle":
			p, err := apps.DecodeConfigPlan(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "secret_set":
			stored, err := decodeStored(fields["data"])
			if err != nil {
				return invalid()
			}
			value = stored

		case "reconcile":
			accepted, err := decodeAccepted(fields["data"])
			if err == nil {
				value = accepted
				break
			}
			report, err := reconcile.DecodeReport(fields["data"])
			if err != nil || !report.DryRun {
				return invalid()
			}
			value = report
		case "plan", "data_prepare_plan":
			p, err := decodePlanned(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "diagnose":
			report, err := diagnose.DecodeReport(fields["data"])
			if err != nil {
				return invalid()
			}
			value = report
		case "status":
			report, err := apps.DecodeReport(fields["data"])
			if err != nil {
				return invalid()
			}
			value = report
		case "rollback":
			p, err := apps.DecodeRollback(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "logs":
			lines, err := logs.DecodeLines(fields["data"])
			if err != nil {
				return invalid()
			}
			value = lines
		case "ping":
			ping, err := strictjson.Object(fields["data"], "server_version", "protocol_versions")
			if err != nil {
				return invalid()
			}
			serverVersion, err := strictjson.Value[string](ping["server_version"])
			if err != nil || serverVersion == "" {
				return invalid()
			}
			versions, err := strictjson.Value[[]int](ping["protocol_versions"])
			if err != nil || len(versions) != 1 || versions[0] != SchemaVersion {
				return invalid()
			}
			value = PingData{serverVersion, versions}
		case "inventory":
			snapshot, err := target.Decode(fields["data"])
			if err != nil {
				return invalid()
			}
			value = snapshot
		case "apply", "resolve":
			accepted, err := decodeAccepted(fields["data"])
			if err != nil {
				return invalid()
			}
			value = accepted
		case "operation":
			status, err := decodeStatus(fields["data"])
			if err != nil {
				return invalid()
			}
			value = status
		}
		return result.Success(command, value), nil
	}
	if !bytes.Equal(bytes.TrimSpace(fields["data"]), []byte("null")) {
		return invalid()
	}
	machine, err := strictjson.Object(fields["error"], "code", "message", "retryable")
	if err != nil {
		return invalid()
	}
	code, err := strictjson.Value[result.Code](machine["code"])
	if err != nil || !result.KnownCode(code) {
		return invalid()
	}
	message, err := strictjson.Value[string](machine["message"])
	if err != nil {
		return invalid()
	}
	retryable, err := strictjson.Value[bool](machine["retryable"])
	if err != nil {
		return invalid()
	}
	response := result.Failure(command, result.New(code, nil))
	if message != response.Error.Message || retryable != response.Error.Retryable {
		return invalid()
	}
	return response, nil
}
