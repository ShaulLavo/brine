package data

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

type RootEvidence struct {
	Root          PersistentRoot    `json:"root"`
	Device        uint64            `json:"device"`
	Inode         uint64            `json:"inode"`
	Filesystem    string            `json:"filesystem"`
	POSIXLocks    bool              `json:"posix_locks"`
	DurableRename bool              `json:"durable_rename"`
	FreeBytes     uint64            `json:"free_bytes"`
	FreeInodes    uint64            `json:"free_inodes"`
	ObservedAt    time.Time         `json:"observed_at"`
	OperatorQuota *QuotaObservation `json:"operator_quota,omitempty"`
}
type QuotaObservation struct {
	LimitBytes uint64 `json:"limit_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	EnforcedBy string `json:"enforced_by"`
}

func (e RootEvidence) Admits(root PersistentRoot, minimum uint64) bool {
	return e.Root == root && e.Device != 0 && e.Inode != 0 && e.POSIXLocks && e.DurableRename && (e.Filesystem == "ext4" || e.Filesystem == "xfs" || e.Filesystem == "btrfs") && !e.ObservedAt.IsZero() && e.FreeBytes >= minimum && e.FreeInodes > 0
}
func OverlappingPaths(a, b string) bool {
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/")
}

// PrepareDirectory allocates only the runner-owned directory hierarchy. It
// never creates a SQLite database or opens a release directory. The caller
// persists its allocation receipt under the host mutation lock before startup.
func PrepareDirectory(b DatabaseBinding) (RootEvidence, error) {
	relative, err := RelativeDirectory(b.IncarnationID, b.DatabaseID)
	if err != nil || relative != b.RelativeDirectory || !ValidRoot(string(b.Root)) {
		return RootEvidence{}, ErrInvalid
	}
	if _, err = InspectRoot(string(b.Root)); err != nil {
		return RootEvidence{}, err
	}
	root, err := os.OpenRoot(string(b.Root))
	if err != nil {
		return RootEvidence{}, err
	}
	defer root.Close()
	current := ""
	for _, component := range strings.Split(relative, "/") {
		current = filepath.Join(current, component)
		err = root.Mkdir(current, 0700)
		if err != nil && !os.IsExist(err) {
			return RootEvidence{}, err
		}
		if err = verifyPrivateTree(string(b.Root), filepath.Join(string(b.Root), current)); err != nil {
			return RootEvidence{}, err
		}
		parent, err := root.Open(filepath.Dir(current))
		if err != nil {
			return RootEvidence{}, err
		}
		err = parent.Sync()
		closeErr := parent.Close()
		if err != nil {
			return RootEvidence{}, err
		}
		if closeErr != nil {
			return RootEvidence{}, closeErr
		}
	}
	return InspectRoot(string(b.Root))
}
