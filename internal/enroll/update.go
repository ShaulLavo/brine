package enroll

import "github.com/ShaulLavo/brine/internal/admission"

// UpdatePlan is the immutable review input. Resume returns this stored plan,
// never a new plan derived from partially replaced artifacts.
type UpdatePlan struct {
	Digest               string               `json:"digest"`
	EnrollmentID         string               `json:"enrollment_id"`
	HostKey              string               `json:"host_key"`
	CandidateSHA256      string               `json:"candidate_sha256"`
	CandidateVersion     string               `json:"candidate_version"`
	RecordedBinarySHA256 string               `json:"recorded_binary_sha256"`
	Artifacts            []UpdateArtifact     `json:"artifacts"`
	Quiescence           admission.Quiescence `json:"quiescence"`
	Bootstrap            bool                 `json:"bootstrap"`
	ControlAbsent        bool                 `json:"control_absent"`
	SchemaFrom           int                  `json:"schema_from"`
	SchemaTo             int                  `json:"schema_to"`
	Migration            bool                 `json:"migration"`
	DaemonReload         bool                 `json:"daemon_reload"`
	SettleEnable         bool                 `json:"settle_enable"`
	ObserverRestart      bool                 `json:"observer_restart"`
	UserDaemonReload     bool                 `json:"user_daemon_reload"`
	StaleProcesses       []string             `json:"stale_processes"`
	DrainBlockers        []string             `json:"drain_blockers"`
}

type UpdateArtifact struct {
	Path   string     `json:"path"`
	Class  string     `json:"class"`
	Action string     `json:"action"`
	Before UpdateFile `json:"before"`
	After  UpdateFile `json:"after"`
}

// UpdateFile distinguishes absence from a present empty file and binds the
// installed inode as well as its bytes and metadata.
type UpdateFile struct {
	Present bool   `json:"present"`
	SHA256  string `json:"sha256,omitempty"`
	Dev     uint64 `json:"dev,omitempty"`
	Ino     uint64 `json:"ino,omitempty"`
	Size    int64  `json:"size,omitempty"`
	Mode    uint32 `json:"mode,omitempty"`
	UID     int    `json:"uid,omitempty"`
	GID     int    `json:"gid,omitempty"`
}

type UpdateResult struct {
	ID         string `json:"id"`
	Committed  bool   `json:"committed"`
	Aborted    bool   `json:"aborted"`
	Retained   string `json:"retained"`
	Quarantine string `json:"quarantine,omitempty"`
}
