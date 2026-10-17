package dispatch

import (
	"bytes"
	"encoding/json"

	"github.com/ShaulLavo/brine/internal/apps"
	"github.com/ShaulLavo/brine/internal/archive"
	"github.com/ShaulLavo/brine/internal/backupcredentials"
	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/cdnoperator"
	enginedata "github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/datainit"
	"github.com/ShaulLavo/brine/internal/datapublish"
	"github.com/ShaulLavo/brine/internal/diagnose"
	"github.com/ShaulLavo/brine/internal/liverestore"
	"github.com/ShaulLavo/brine/internal/logs"
	"github.com/ShaulLavo/brine/internal/nativecapture"
	"github.com/ShaulLavo/brine/internal/nativematerial"
	"github.com/ShaulLavo/brine/internal/reconcile"
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
		case "cdn_context":
			planning, err := decodeCDNPlanning(fields["data"])
			if err != nil {
				return invalid()
			}
			value = planning
		case "cdn_plan":
			var p cdn.Plan
			if cdnoperator.DecodeExact(fields["data"], &p) != nil || p.Validate() != nil {
				return invalid()
			}
			value = p
		case "cdn_apply", "cdn_next", "cdn_seal", "cdn_result", "cdn_inspect", "cdn_proof":
			var progress cdnoperator.Progress
			if json.Unmarshal(fields["data"], &progress) != nil {
				return invalid()
			}
			value = progress
		case "cdn_status":
			status, err := decodeCDNStatusResponse(fields["data"])
			if err != nil {
				return invalid()
			}
			value = status
		case "archive_maintenance_plan":
			p, err := decodeArchiveMaintenancePlanResponse(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "archive_status":
			p, err := decodeArchiveStatusResponse(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "archive_credentials_plan":
			p, err := decodeArchiveCredentialPlanResponse(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "archive_credentials_set":
			p, err := decodeArchiveCredentialReceiptResponse(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "archive_maintenance_apply", "archive_maintenance_resolve":
			accepted, err := decodeAccepted(fields["data"])
			if err != nil {
				return invalid()
			}
			value = accepted

		case "module_publish_plan":
			view, err := datapublish.DecodeReviewView(fields["data"])
			if err != nil {
				return invalid()
			}
			value = view
		case "module_publish_inspect":
			view, err := datapublish.DecodeInspection(fields["data"])
			if err != nil {
				return invalid()
			}
			value = view
		case "native_material_plan":
			p, err := nativematerial.DecodeActivationPlan(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "native_material_activate":
			r, err := nativematerial.DecodeActivationReceipt(fields["data"])
			if err != nil {
				return invalid()
			}
			value = r
		case "native_material_set", "native_material_status":
			receipt, err := nativematerial.DecodeReceipt(fields["data"])
			if err != nil {
				return invalid()
			}
			value = receipt
		case "native_retire_plan":
			var p archive.Plan
			if json.Unmarshal(fields["data"], &p) != nil || !p.Valid() {
				return invalid()
			}
			value = p
		case "native_retire_inspect":
			var i archive.Inspection
			if json.Unmarshal(fields["data"], &i) != nil || !enginedata.ValidID(string(i.Disposition.ArchiveID)) || i.Disposition.Sequence == 0 {
				return invalid()
			}
			value = i
		case "native_live_restore_review_resolution":
			r, err := liverestore.DecodeResolution(fields["data"])
			if err != nil {
				return invalid()
			}
			value = r
		case "native_live_restore_plan":
			p, err := liverestore.DecodePlan(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "native_live_restore_inspect":
			p, err := liverestore.DecodeProgress(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "native_point":
			point, err := nativecapture.DecodePointInspection(fields["data"])
			if err != nil {
				return invalid()
			}
			value = point
		case "deploy_checkpoint":
			checked, err := decodeDeploymentCheckpoint(fields["data"])
			if err != nil {
				return invalid()
			}
			value = checked
		case "deploy_operation":
			recovered, err := decodeDeploymentOperation(fields["data"])
			if err != nil {
				return invalid()
			}
			value = recovered
		case "deploy_state":
			state, err := decodeDeploymentState(fields["data"])
			if err != nil {
				return invalid()
			}
			value = state
		case "data_init_plan":
			var p datainit.Plan
			decoder := json.NewDecoder(bytes.NewReader(fields["data"]))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&p) != nil || !p.Valid() {
				return invalid()
			}
			value = p
		case "data_init_apply", "restore_test", "backup_credentials_set", "native_recovery_set", "native_backup_activate", "native_capture", "native_prepare", "native_prepare_abandon", "native_restore_test", "module_publish_apply", "module_publish_resolve", "native_retire", "native_retire_resolve", "native_live_restore_resolve", "native_live_restore", "native_backup_resolve":
			accepted, err := decodeAccepted(fields["data"])
			if err != nil {
				return invalid()
			}
			value = accepted
		case "native_backup_inspect":
			view, err := DecodeNativeBackupInspectionView(fields["data"])
			if err != nil {
				return invalid()
			}
			value = view
		case "native_backup_review":
			review, err := nativecapture.DecodeBackupResolutionReview(fields["data"])
			if err != nil {
				return invalid()
			}
			value = review
		case "native_backup_status":
			status, err := nativecapture.DecodeBackupStatus(fields["data"])
			if err != nil {
				return invalid()
			}
			value = status
		case "native_backup_policy_plan":
			view, err := DecodeNativeBackupPolicyPlanView(fields["data"])
			if err != nil {
				return invalid()
			}
			value = view
		case "backup_credentials_plan":
			p, err := backupcredentials.DecodePlan(fields["data"])
			if err != nil {
				return invalid()
			}
			value = p
		case "config_set", "lifecycle", "engine_stop":
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
