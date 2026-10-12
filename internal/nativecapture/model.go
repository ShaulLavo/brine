// Package nativecapture validates version-pinned capture records. Structural
// records do not prove source ownership, physical replay, or mutation authority.
package nativecapture

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
)

const Format = "brine-native-capture-v1"
const EngineVersion = "2.11.0"
const UpstreamCommit = "6b6e0ab19e4f8f52472aa4379552e9593c8f53bb"

var (
	ErrInvalid   = errors.New("invalid native capture record")
	ErrLimit     = errors.New("native capture limit exceeded")
	ErrSelection = errors.New("native capture selection unavailable")
	idPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

type Scope struct {
	BindingID string `json:"binding_id"`
	EpochID   string `json:"epoch_id"`
	CaptureID string `json:"capture_id"`
	ReplicaID uint64 `json:"replica_id"`
}

func (s Scope) valid() bool {
	return idPattern.MatchString(s.BindingID) && idPattern.MatchString(s.EpochID) && idPattern.MatchString(s.CaptureID)
}

type Limits struct {
	Bytes, FileBytes, ExpandedBytes, ExpandedFileBytes uint64
	Files                                              int
}

func DefaultLimits() Limits {
	return Limits{Bytes: 64 << 20, FileBytes: 32 << 20, ExpandedBytes: 128 << 20, ExpandedFileBytes: 64 << 20, Files: 8192}
}
func (l Limits) valid() bool {
	return l.Bytes > 0 && l.Bytes <= 256<<20 && l.FileBytes > 0 && l.FileBytes <= l.Bytes && l.ExpandedBytes > 0 && l.ExpandedBytes <= 256<<20 && l.ExpandedFileBytes > 0 && l.ExpandedFileBytes <= l.ExpandedBytes && l.Files > 0 && l.Files <= 8192
}

// Member records an owned-descriptor observation supplied by the future host
// bridge. Constructing its syntax does not establish a live source seal.
type Member struct {
	Path          string `json:"path"`
	Bytes         uint64 `json:"bytes"`
	ExpandedBytes uint64 `json:"expanded_bytes"`
	SHA256        string `json:"sha256"`
	Device        uint64 `json:"device"`
	Inode         uint64 `json:"inode"`
	MountID       uint64 `json:"mount_id"`
	UID           uint32 `json:"uid"`
	Mode          uint32 `json:"mode"`
	Links         uint64 `json:"links"`
}

type Inventory struct {
	scope   Scope
	members []Member
}

func (i Inventory) Scope() Scope      { return i.scope }
func (i Inventory) Members() []Member { return slices.Clone(i.members) }

func NewInventory(scope Scope, members []Member, limits Limits) (Inventory, error) {
	if !scope.valid() || !limits.valid() || len(members) == 0 {
		return Inventory{}, ErrInvalid
	}
	if len(members) > limits.Files {
		return Inventory{}, ErrLimit
	}
	owned := slices.Clone(members)
	slices.SortFunc(owned, func(a, b Member) int { return comparePath(a.Path, b.Path) })
	var stored, expanded uint64
	for index, member := range owned {
		if !validMember(scope, member) || index > 0 && owned[index-1].Path == member.Path {
			return Inventory{}, ErrInvalid
		}
		if member.Bytes > limits.FileBytes || member.ExpandedBytes > limits.ExpandedFileBytes {
			return Inventory{}, ErrLimit
		}
		stored += member.Bytes
		expanded += member.ExpandedBytes
		if stored > limits.Bytes || expanded > limits.ExpandedBytes {
			return Inventory{}, ErrLimit
		}
	}
	inventory := Inventory{scope: scope, members: owned}
	raw, err := json.Marshal(inventory)
	if err != nil || len(raw) > 2<<20 {
		return Inventory{}, ErrLimit
	}
	return inventory, nil
}
