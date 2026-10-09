package enroll

type UndoResult struct {
	Removed        bool `json:"removed"`
	RetainedPolicy bool `json:"retained_policy,omitempty"`
}
