//go:build linux

package enroll

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ShaulLavo/brine/internal/result"
)

const permissionsDirectory = "/var/lib/brine-permissions"

func PermissionsHostOperation(ctx context.Context, request PermissionsRequest) (_ PermissionsResponse, err error) {
	if os.Geteuid() != 0 || len(request.IdentityKey) != 32 {
		return PermissionsResponse{}, result.New(result.PolicyRefused, nil)
	}
	defer clear(request.IdentityKey)
	enrollmentLock, err := permissionLock(recordDir + "/lock")
	if err != nil {
		return PermissionsResponse{}, err
	}
	defer func() { err = errors.Join(err, enrollmentLock.Close()) }()
	record, err := readRecord()
	if err != nil || record.ID == "" || record.Journal.Phase != Enrolled {
		return PermissionsResponse{}, errPermissionsRefused
	}
	if err = permissionIdentity(request); err != nil {
		return PermissionsResponse{}, err
	}
	if err = protectedParents(permissionsDirectory); err != nil {
		return PermissionsResponse{}, err
	}
	if err = os.Mkdir(permissionsDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return PermissionsResponse{}, err
	}
	if err = protectedParents(permissionsDirectory + "/lock"); err != nil {
		return PermissionsResponse{}, err
	}
	info, err := os.Lstat(permissionsDirectory)
	if err != nil || info.Mode().Perm() != 0700 {
		return PermissionsResponse{}, errPermissionsRefused
	}
	lock, err := permissionLock(permissionsDirectory + "/lock")
	if err != nil {
		return PermissionsResponse{}, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	service := permissionService{store: diskPermissions{}, enrollment: record.ID}
	return service.run(ctx, request)
}

func permissionIdentity(request PermissionsRequest) error {
	if err := protectedParents(sshIdentityPath); err != nil {
		return err
	}
	info, err := os.Lstat(sshIdentityPath)
	if err != nil {
		return err
	}
	if err = operatorPolicyMetadata(info); err != nil {
		return err
	}
	identity, err := boundedRead(sshIdentityPath)
	if err != nil || subtle.ConstantTimeCompare(identity, request.IdentityKey) != 1 {
		return errPermissionsRefused
	}
	raw, err := boundedRead("/etc/ssh/ssh_host_ed25519_key.pub")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw))
	if len(fields) < 2 {
		return errPermissionsRefused
	}
	actual, err := PublicKey(strings.Join(fields[:2], " "))
	if err != nil || actual != request.HostKey {
		return errPermissionsRefused
	}
	return nil
}

func permissionLock(path string) (*os.File, error) {
	if err := protectedParents(path); err != nil {
		return nil, err
	}
	// #nosec G304 -- Callers supply fixed root-owned lock paths; parents and the opened inode are checked.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil {
		err = permissionRecordMetadata(info)
	}
	if err == nil {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	}
	if err != nil {
		return nil, result.New(result.Conflict, errors.Join(err, f.Close()))
	}
	return f, nil
}

func permissionRecordMetadata(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || st.Uid != 0 || st.Nlink != 1 {
		return errPermissionsRefused
	}
	return nil
}

type diskPermissions struct{}

func (diskPermissions) ReadPolicy() ([]byte, error) { return (diskOperatorPolicy{}).Read() }
func (diskPermissions) WritePolicy(expected string, raw []byte) error {
	before, err := (diskOperatorPolicy{}).Read()
	if err != nil {
		return err
	}
	if permissionHash(before) != expected {
		return result.New(result.Conflict, nil)
	}
	return atomicFile(operatorPolicyPath, raw, 0644)
}
func (diskPermissions) SavePlan(p permissionPlanRecord) error {
	return savePermissionRecord("plan-"+p.Plan.ID+".json", p)
}
func (diskPermissions) LoadPlan(id string) (permissionPlanRecord, error) {
	var record permissionPlanRecord
	err := readPermissionRecord("plan-"+id+".json", &record)
	return record, err
}
func (diskPermissions) SaveOperation(op PermissionsOperation) error {
	return savePermissionRecord("operation-"+op.ID+".json", op)
}
func (diskPermissions) LoadOperation(key string) (PermissionsOperation, bool, error) {
	var op PermissionsOperation
	err := readPermissionRecord("operation-"+key+".json", &op)
	if errors.Is(err, os.ErrNotExist) {
		return op, false, nil
	}
	if err == nil && !validPermissionsOperation(op, key) {
		return op, false, errPermissionsRefused
	}
	return op, err == nil, err
}

func validPermissionsOperation(op PermissionsOperation, key string) bool {
	return op.ID == key && permissionID.MatchString(op.PlanID) && permissionID.MatchString(op.BeforeHash) && permissionID.MatchString(op.AfterHash) && (op.State == "intent" || op.State == "succeeded")
}
func (d diskPermissions) PendingOperation() (string, error) {
	entries, err := os.ReadDir(permissionsDirectory)
	if err != nil {
		return "", err
	}
	pending := ""
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "operation-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		key := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "operation-"), ".json")
		if !permissionKey.MatchString(key) {
			return "", errPermissionsRefused
		}
		op, _, readErr := d.LoadOperation(key)
		if readErr != nil {
			return "", readErr
		}
		if op.State != "intent" {
			continue
		}
		if pending != "" {
			return "", errPermissionsRefused
		}
		pending = key
	}
	return pending, nil
}
func readPermissionRecord(name string, value any) error {
	path := filepath.Join(permissionsDirectory, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err = permissionRecordMetadata(info); err != nil {
		return err
	}
	if info.Size() > 4<<20 {
		return errPermissionsRefused
	}
	raw, err := boundedRead(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}
func savePermissionRecord(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicFile(filepath.Join(permissionsDirectory, name), raw, 0600)
}
