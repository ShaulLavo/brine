package nativecapture

import (
	"fmt"
	"path"
	"slices"
	"strconv"
	"strings"
)

func comparePath(a, b string) int { return strings.Compare(a, b) }

type memberPath struct {
	kind              string
	offset            uint64
	snapshotDirectory string
}

func parseMember(scope Scope, name string) (memberPath, error) {
	if path.Clean(name) != name || strings.HasPrefix(name, "/") || len(name) > 240 {
		return memberPath{}, ErrInvalid
	}
	parts := strings.Split(name, "/")
	if len(parts) < 4 || parts[0] != "replicas" || parts[1] != strconv.FormatUint(scope.ReplicaID, 10) {
		return memberPath{}, ErrInvalid
	}
	if parts[2] == "clog" && len(parts) == 4 {
		offset, err := paddedOffset(parts[3], ".stdb.log")
		return memberPath{kind: "log", offset: offset}, err
	}
	if parts[2] != "snapshots" || len(parts) < 5 {
		return memberPath{}, ErrInvalid
	}
	offset, err := paddedOffset(parts[3], ".snapshot_dir")
	if err != nil {
		return memberPath{}, err
	}
	directory := strings.Join(parts[:4], "/")
	if len(parts) == 5 && parts[4] == fmt.Sprintf("%020d.snapshot_bsatn", offset) {
		return memberPath{kind: "snapshot", offset: offset, snapshotDirectory: directory}, nil
	}
	if len(parts) != 7 || parts[4] != "objects" || len(parts[5]) != 2 || len(parts[6]) != 62 {
		return memberPath{}, ErrInvalid
	}
	for _, part := range parts[5:] {
		if part == "" || strings.IndexFunc(part, nonHex) != -1 {
			return memberPath{}, ErrInvalid
		}
	}
	return memberPath{kind: "object", offset: offset, snapshotDirectory: directory}, nil
}

func nonHex(c rune) bool { return c < '0' || c > '9' && c < 'a' || c > 'f' }
func paddedOffset(name, suffix string) (uint64, error) {
	if !strings.HasSuffix(name, suffix) {
		return 0, ErrInvalid
	}
	value := strings.TrimSuffix(name, suffix)
	number, err := strconv.ParseUint(value, 10, 64)
	if err != nil || len(value) != 20 || fmt.Sprintf("%020d", number) != value {
		return 0, ErrInvalid
	}
	return number, nil
}

func validMember(scope Scope, m Member) bool {
	if m.Mode != 0600 || m.Links != 1 || m.Device == 0 || m.Inode == 0 || m.MountID == 0 || m.Bytes == 0 || m.ExpandedBytes == 0 || !hashPattern.MatchString(m.SHA256) {
		return false
	}
	_, err := parseMember(scope, m.Path)
	return err == nil
}

// Selection is a structural copy plan only. Its snapshot and log filenames do
// not assert manifest completeness, native framing, CRC, or recovered state.
type Selection struct {
	scope    Scope
	kind     string
	snapshot uint64
	members  []Member
	tail     Member
	hasTail  bool
}

func (s Selection) Scope() Scope                   { return s.scope }
func (s Selection) Kind() string                   { return s.kind }
func (s Selection) SnapshotOffset() (uint64, bool) { return s.snapshot, s.kind == "snapshot" }
func (s Selection) Members() []Member              { return slices.Clone(s.members) }
func (s Selection) TailMember() (Member, bool)     { return s.tail, s.hasTail }

func Select(inventory Inventory, limits Limits) (Selection, error) {
	checked, err := NewInventory(inventory.scope, inventory.members, limits)
	if err != nil {
		return Selection{}, err
	}
	logs, snapshots := groupMembers(checked)
	if len(snapshots) == 0 {
		return selectLogs(checked.scope, logs)
	}
	snapshot := snapshots[len(snapshots)-1]
	members := snapshotMembers(checked, snapshot)
	if len(logs) == 0 {
		return Selection{scope: checked.scope, kind: "snapshot", snapshot: snapshot, members: members}, nil
	}
	start := suffixStart(checked.scope, logs, snapshot)
	if start < 0 {
		return Selection{}, ErrSelection
	}
	members = append(members, logs[start:]...)
	return Selection{scope: checked.scope, kind: "snapshot", snapshot: snapshot, members: members, tail: logs[len(logs)-1], hasTail: true}, nil
}

func selectLogs(scope Scope, logs []Member) (Selection, error) {
	if len(logs) == 0 {
		return Selection{}, ErrSelection
	}
	first, _ := parseMember(scope, logs[0].Path)
	if first.offset != 0 {
		return Selection{}, ErrSelection
	}
	return Selection{scope: scope, kind: "logs", members: logs, tail: logs[len(logs)-1], hasTail: true}, nil
}

func groupMembers(inventory Inventory) ([]Member, []uint64) {
	var logs []Member
	var snapshots []uint64
	for _, member := range inventory.members {
		parsed, _ := parseMember(inventory.scope, member.Path)
		if parsed.kind == "log" {
			logs = append(logs, member)
		}
		if parsed.kind == "snapshot" {
			snapshots = append(snapshots, parsed.offset)
		}
	}
	slices.Sort(snapshots)
	return logs, snapshots
}

func suffixStart(scope Scope, logs []Member, snapshot uint64) int {
	index := -1
	for i, member := range logs {
		parsed, _ := parseMember(scope, member.Path)
		if snapshot < ^uint64(0) && parsed.offset == snapshot+1 {
			return i
		}
		if parsed.offset <= snapshot {
			index = i
		}
	}
	return index
}

func snapshotMembers(inventory Inventory, offset uint64) []Member {
	var members []Member
	for _, member := range inventory.members {
		parsed, _ := parseMember(inventory.scope, member.Path)
		if parsed.kind != "log" && parsed.offset == offset {
			members = append(members, member)
		}
	}
	return members
}
