package datapublish

type StageFailure struct {
	Action string `json:"action"`
	Code   string `json:"code"`
}

func (f StageFailure) Valid() bool {
	if f.Action != "preflight" && f.Action != "publish" && f.Action != "create" {
		return false
	}
	if GuestStageFailureCode(f.Code) {
		return true
	}
	switch f.Code {
	case "stage_run_failed", "stage_disposal_failed", "stage_source_changed", "stage_result_decode", "stage_result_mismatch", "stage_preflight_record", "stage_publication_record":
		return true
	default:
		return false
	}
}

func GuestStageFailureCode(code string) bool {
	switch code {
	case "guest_containment", "guest_input", "guest_source", "guest_credential", "guest_child_start", "guest_child_bind", "guest_listener", "guest_publisher", "guest_operation", "guest_child_stop", "guest_output", "guest_create_artifact", "guest_create_publish", "guest_create_endpoint", "guest_create_expected", "guest_create_observe":
		return true
	default:
		return false
	}
}
