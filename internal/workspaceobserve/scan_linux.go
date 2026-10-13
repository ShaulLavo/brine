//go:build linux

package workspaceobserve

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const enumerationLimit = 4096

func boundedRead(root *os.Root, path string, limit int64) ([]byte, error) {
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrUnknown
	}
	return raw, nil
}
func processStart(root *os.Root, pid string) (uint64, error) {
	raw, err := boundedRead(root, filepath.Join(pid, "stat"), 64<<10)
	if err != nil {
		return 0, err
	}
	end := strings.LastIndex(string(raw), ")")
	if end < 0 || strings.TrimSpace(strings.SplitN(string(raw), "(", 2)[0]) != filepath.Base(pid) {
		return 0, ErrUnknown
	}
	fields := strings.Fields(string(raw[end+1:]))
	if len(fields) < 20 {
		return 0, ErrUnknown
	}
	ticks, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil || ticks == 0 {
		return 0, ErrUnknown
	}
	return ticks, nil
}
func processUID(root *os.Root, pid string) (uint32, error) {
	raw, err := boundedRead(root, filepath.Join(pid, "status"), 64<<10)
	if err != nil {
		return 0, err
	}
	found := false
	var uid uint32
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.HasPrefix(line, "Uid:") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
		if found || len(fields) != 4 {
			return 0, ErrUnknown
		}
		value, err := strconv.ParseUint(fields[1], 10, 32)
		if err != nil || strconv.FormatUint(value, 10) != fields[1] {
			return 0, ErrUnknown
		}
		uid, found = uint32(value), true
	}
	if !found {
		return 0, ErrUnknown
	}
	return uid, nil
}
func contains(workspace, path string) bool {
	path = strings.TrimSuffix(path, " (deleted)")
	return filepath.IsAbs(path) && (path == workspace || strings.HasPrefix(path, workspace+"/"))
}
func (s scope) scan(ctx context.Context, path string, held directoryIdentity, caller int32) string {
	root, err := os.OpenRoot("/proc")
	if err != nil {
		return "unknown"
	}
	defer func() { _ = root.Close() }()
	boot, err := boundedRead(root, "sys/kernel/random/boot_id", 128)
	if err != nil || len(boot) != 37 {
		return "unknown"
	}
	peers, err := s.peerProcesses(ctx, root)
	if err != nil {
		return "unknown"
	}
	for pid := range peers {
		state := s.scanProcess(ctx, root, pid, path, held, caller)
		if state != "absent" {
			return state
		}
	}
	afterPeers, err := s.peerProcesses(ctx, root)
	if err != nil || len(afterPeers) != len(peers) {
		return "unknown"
	}
	for pid, start := range peers {
		if afterPeers[pid] != start {
			return "unknown"
		}
	}
	after, err := boundedRead(root, "sys/kernel/random/boot_id", 128)
	if err != nil || string(after) != string(boot) || ctx.Err() != nil {
		return "unknown"
	}
	return "absent"
}
func (s scope) peerProcesses(ctx context.Context, root *os.Root) (map[string]uint64, error) {
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	entries, err := directory.ReadDir(enumerationLimit + 1)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > enumerationLimit {
		return nil, ErrUnknown
	}
	peers := map[string]uint64{}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ErrUnknown
		}
		number, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || number == 0 {
			continue
		}
		if strconv.FormatUint(number, 10) != entry.Name() {
			return nil, ErrUnknown
		}
		uid, err := processUID(root, entry.Name())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, ErrUnknown
		}
		if uid != s.uid {
			continue
		}
		start, err := processStart(root, entry.Name())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, ErrUnknown
		}
		peers[entry.Name()] = start
	}
	return peers, nil
}
func (s scope) scanProcess(ctx context.Context, root *os.Root, pid, path string, held directoryIdentity, caller int32) string {
	directory, err := openDirectory(path)
	if err != nil {
		return "unknown"
	}
	defer func() { _ = directory.Close() }()
	members, err := workspaceMembers(ctx, directory, s.uid, held)
	if err != nil {
		return "unknown"
	}
	start, err := processStart(root, pid)
	if os.IsNotExist(err) {
		return "absent"
	}
	if err != nil {
		return "unknown"
	}
	tasks, err := processTasks(root, pid)
	if err != nil {
		return "unknown"
	}
	state := "absent"
	incomplete := false
	for task, ticks := range tasks {
		observed := s.scanTask(ctx, root, filepath.Join(pid, "task", task), ticks, path, held, members, caller)
		if observed == "unknown" {
			incomplete = true
		}
		if observed == "busy" {
			state = observed
		}
	}
	after, err := processStart(root, pid)
	if err != nil || after != start || ctx.Err() != nil {
		return "unknown"
	}
	if state == "busy" {
		return state
	}
	afterTasks, err := processTasks(root, pid)
	if err != nil || len(afterTasks) != len(tasks) {
		return "unknown"
	}
	for task, ticks := range tasks {
		if afterTasks[task] != ticks {
			return "unknown"
		}
	}
	if incomplete && state != "busy" {
		return "unknown"
	}
	return state
}
func processTasks(root *os.Root, pid string) (map[string]uint64, error) {
	directory, err := root.Open(filepath.Join(pid, "task"))
	if err != nil {
		return nil, ErrUnknown
	}
	entries, err := directory.ReadDir(enumerationLimit + 1)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > enumerationLimit {
		return nil, ErrUnknown
	}
	tasks := map[string]uint64{}
	for _, entry := range entries {
		number, err := strconv.ParseUint(entry.Name(), 10, 32)
		if err != nil || number == 0 || strconv.FormatUint(number, 10) != entry.Name() {
			return nil, ErrUnknown
		}
		start, err := processStart(root, filepath.Join(pid, "task", entry.Name()))
		if err != nil {
			return nil, ErrUnknown
		}
		tasks[entry.Name()] = start
	}
	if len(tasks) == 0 {
		return nil, ErrUnknown
	}
	return tasks, nil
}
func (s scope) scanTask(ctx context.Context, root *os.Root, task string, start uint64, path string, held directoryIdentity, members map[fileIdentity]bool, caller int32) string {
	if ctx.Err() != nil {
		return "unknown"
	}
	uid, err := processUID(root, task)
	if err != nil {
		return "unknown"
	}
	if uid != s.uid {
		return "absent"
	}
	cwd, err := root.Readlink(filepath.Join(task, "cwd"))
	if err != nil {
		return "unknown"
	}
	state := "absent"
	if contains(path, cwd) {
		state = "busy"
	}
	directory, err := root.Open(filepath.Join(task, "fd"))
	if err != nil {
		return "unknown"
	}
	entries, err := directory.ReadDir(enumerationLimit + 1)
	closeErr := directory.Close()
	if err != nil && !errors.Is(err, io.EOF) || closeErr != nil || len(entries) > enumerationLimit {
		return "unknown"
	}
	for _, entry := range entries {
		consumer, err := consumerFD(root, task, entry.Name(), path, held, members, caller)
		if err != nil {
			return "unknown"
		}
		if consumer {
			state = "busy"
		}
	}
	after, err := processStart(root, task)
	afterUID, uidErr := processUID(root, task)
	if err != nil || uidErr != nil || after != start || afterUID != uid || ctx.Err() != nil {
		return "unknown"
	}
	return state
}
func consumerFD(root *os.Root, task, number, path string, held directoryIdentity, members map[fileIdentity]bool, caller int32) (bool, error) {
	value, err := strconv.ParseUint(number, 10, 32)
	if err != nil || strconv.FormatUint(value, 10) != number {
		return false, ErrUnknown
	}
	name := filepath.Join(task, "fd", number)
	target, err := root.Readlink(name)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var stat unix.Statx_t
	raw, err := root.Open(".")
	if err != nil {
		return false, err
	}
	defer func() { _ = raw.Close() }()
	err = unix.Statx(int(raw.Fd()), name, 0, unix.STATX_BASIC_STATS|unix.STATX_MNT_ID, &stat)
	if errors.Is(err, unix.ENOENT) {
		return false, nil
	}
	if err != nil || stat.Mask&unix.STATX_MNT_ID == 0 {
		return false, ErrUnknown
	}
	member := directoryIdentity{unix.Mkdev(stat.Dev_major, stat.Dev_minor), stat.Ino, stat.Mnt_id}
	if !contains(path, target) && !members[fileIdentity{member.Device, member.Inode}] {
		return false, nil
	}
	pid := strings.Split(task, "/")[0]
	if pid == strconv.FormatInt(int64(caller), 10) && strings.TrimSuffix(target, " (deleted)") == path && member == held && stat.Mode == unix.S_IFDIR|0700 {
		return false, nil
	}
	return true, nil
}
