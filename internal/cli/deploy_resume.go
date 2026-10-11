package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ShaulLavo/brine/internal/jobs"
	"github.com/ShaulLavo/brine/internal/result"
	"golang.org/x/sys/unix"
)

type deploymentStep struct {
	Database    string `json:"database,omitempty"`
	Name        string `json:"name"`
	PlanID      string `json:"plan_id,omitempty"`
	Key         string `json:"idempotency_key,omitempty"`
	OperationID string `json:"operation_id,omitempty"`
	Complete    bool   `json:"complete"`
}
type deploymentJournal struct {
	ReviewHash string           `json:"review_hash"`
	Steps      []deploymentStep `json:"steps"`
}
type deploymentResume struct {
	root    *os.Root
	lock    *os.File
	journal deploymentJournal
}

func (f operationFlags) openDeploymentResume(identity []byte) (*deploymentResume, error) {
	dir := f.configDir
	if dir == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		dir = filepath.Join(base, "brine", "targets")
	}
	sum := sha256.Sum256(identity)
	dir = filepath.Join(dir, "deploys", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, result.New(result.InvalidUsage, nil)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	closeRoot := true
	defer func() {
		if closeRoot {
			_ = root.Close()
		}
	}()
	// O_NOFOLLOW rejects a replaced lock file; the journal uses the confined root.
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW, 0600) // #nosec G304 -- Private client-selected state directory, no symlink lock.

	if err != nil {
		return nil, err
	}
	lockInfo, err := lock.Stat()
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0600 {
		_ = lock.Close()
		return nil, result.New(result.InvalidUsage, nil)
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, result.New(result.Conflict, nil)
	}
	resume := &deploymentResume{root: root, lock: lock, journal: deploymentJournal{Steps: []deploymentStep{}}}
	closeRoot = false
	file, err := root.Open("journal.json")
	if errors.Is(err, os.ErrNotExist) {
		return resume, nil
	}
	if err != nil {
		resume.close()
		return nil, err
	}
	defer func() { _ = file.Close() }()
	entry, err := root.Lstat("journal.json")
	if err != nil || !entry.Mode().IsRegular() {
		resume.close()
		return nil, result.New(result.InvalidUsage, nil)
	}
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 65536 {
		resume.close()
		return nil, result.New(result.InvalidUsage, nil)
	}
	raw, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil || len(raw) > 65536 || json.Unmarshal(raw, &resume.journal) != nil {
		resume.close()
		return nil, result.New(result.InvalidUsage, nil)
	}
	if !jobs.ValidPlanID(resume.journal.ReviewHash) || len(resume.journal.Steps) > 34 || len(resume.journal.Steps) < 2 {
		resume.close()
		return nil, result.New(result.InvalidUsage, nil)
	}
	for i, step := range resume.journal.Steps {
		validName := i == 0 && step.Name == "preparation" || i == len(resume.journal.Steps)-1 && step.Name == "release" || i > 0 && i < len(resume.journal.Steps)-1 && (step.Name == "credentials" || step.Name == "initialization")
		if !validName || step.PlanID != "" && !jobs.ValidPlanID(step.PlanID) || step.Key != "" && !jobs.ValidID(step.Key) || step.OperationID != "" && !jobs.ValidID(step.OperationID) || step.OperationID != "" && step.PlanID == "" {
			resume.close()
			return nil, result.New(result.InvalidUsage, nil)
		}
	}
	return resume, nil
}
func (r *deploymentResume) close() {
	_ = unix.Flock(int(r.lock.Fd()), unix.LOCK_UN)
	_ = r.lock.Close()
	_ = r.root.Close()
}
func (r *deploymentResume) save() error {
	raw, err := json.Marshal(r.journal)
	if err != nil {
		return err
	}
	file, err := r.root.OpenFile("journal.next", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		if err = r.root.Remove("journal.next"); err != nil {
			return err
		}
		file, err = r.root.OpenFile("journal.next", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	}
	if err != nil {
		return err
	}
	_, writeErr := file.Write(raw)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	if err = r.root.Rename("journal.next", "journal.json"); err != nil {
		return err
	}
	dir, err := r.root.Open(".")
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	return dir.Sync()
}
