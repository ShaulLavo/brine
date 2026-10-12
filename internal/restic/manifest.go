package restic

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

type Member struct {
	Path   string `json:"path"`
	Type   string `json:"type"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type manifestDocument struct {
	Version  int      `json:"version"`
	Identity Identity `json:"identity"`
	Members  []Member `json:"members"`
}

type Manifest struct {
	document manifestDocument
	raw      []byte
}

func (m Manifest) Bytes() []byte      { return slices.Clone(m.raw) }
func (m Manifest) Members() []Member  { return slices.Clone(m.document.Members) }
func (m Manifest) SHA256() string     { return digest(m.raw) }
func (m Manifest) Identity() Identity { return m.document.Identity }

// MatchesTree compares an actual rooted tree with this complete member list.
// It validates content only and supplies no remote or mutation authority.
func (m Manifest) MatchesTree(ctx context.Context, root *os.Root) bool {
	if root == nil || ctx.Err() != nil || m.admit(DefaultLimits()) != nil {
		return false
	}
	identity, err := root.Stat(".")
	return err == nil && identity.IsDir() && verifyMemberSet(ctx, root, identity, m, DefaultLimits()) == nil
}
func digest(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func newManifest(identity Identity, members []Member, limits Limits) (Manifest, error) {
	if !limits.valid() || !identity.valid() {
		return Manifest{}, ErrInvalid
	}
	if len(members) > limits.Members {
		return Manifest{}, ErrLimit
	}
	members = slices.Clone(members)
	if members == nil {
		members = []Member{}
	}
	slices.SortFunc(members, func(a, b Member) int { return strings.Compare(a.Path, b.Path) })
	parents := map[string]bool{".": true}
	var total int64
	previous := ""
	for _, member := range members {
		if member.Size > limits.FileBytes {
			return Manifest{}, ErrLimit
		}
		if !validMember(member, limits) || member.Path == previous || !parents[path.Dir(member.Path)] {
			return Manifest{}, ErrInvalid
		}
		previous = member.Path
		parents[member.Path] = member.Type == "directory"
		total += member.Size
		if total > limits.Bytes {
			return Manifest{}, ErrLimit
		}
	}
	doc := manifestDocument{Version: 1, Identity: identity, Members: members}
	raw, err := json.Marshal(doc)
	if err != nil || len(raw) > manifestLimit {
		return Manifest{}, ErrLimit
	}
	return Manifest{document: doc, raw: raw}, nil
}

func validMember(m Member, l Limits) bool {
	if !validPath(m.Path) || m.Size < 0 || m.Size > l.FileBytes {
		return false
	}
	if m.Type == "directory" {
		return m.Mode == 0700 && m.Size == 0 && m.SHA256 == ""
	}
	return m.Type == "file" && m.Mode == 0600 && hashPattern.MatchString(m.SHA256)
}

func validPath(p string) bool {
	if p == "." || len(p) == 0 || len(p) > 240 || path.Clean(p) != p || strings.HasPrefix(p, "/") || !pathPattern.MatchString(p) {
		return false
	}
	parts := strings.Split(p, "/")
	if len(parts) > 16 {
		return false
	}
	for _, part := range parts {
		if part == ".." || len(part) > 100 {
			return false
		}
	}
	return true
}

func DecodeManifest(raw []byte, limits Limits) (Manifest, error) {
	if len(raw) > manifestLimit {
		return Manifest{}, ErrLimit
	}
	fields, err := strictjson.Object(raw, "version", "identity", "members")
	if err != nil {
		return Manifest{}, ErrInvalid
	}
	version, err := strictjson.Value[int](fields["version"])
	if err != nil || version != 1 {
		return Manifest{}, ErrInvalid
	}
	if _, err := strictjson.Object(fields["identity"], "binding_id", "epoch_id", "capture_id", "module_sha256", "checkpoint_sha256"); err != nil {
		return Manifest{}, ErrInvalid
	}
	identity, err := strictjson.Value[Identity](fields["identity"])
	if err != nil {
		return Manifest{}, ErrInvalid
	}
	members, err := decodeMembers(fields["members"], limits)
	if err != nil {
		return Manifest{}, err
	}
	return newManifest(identity, members, limits)
}

func decodeMembers(raw []byte, limits Limits) ([]Member, error) {
	values, err := strictjson.Value[[]json.RawMessage](raw)
	if err != nil {
		return nil, ErrInvalid
	}
	if len(values) > limits.Members {
		return nil, ErrLimit
	}
	members := make([]Member, 0, len(values))
	for _, value := range values {
		fields, err := strictjson.Object(value, "path", "type", "mode", "size", "sha256")
		if err != nil || hasNull(fields) {
			return nil, ErrInvalid
		}
		member, err := strictjson.Value[Member](value)
		if err != nil {
			return nil, ErrInvalid
		}
		members = append(members, member)
	}
	return members, nil
}

func (m Manifest) admit(limits Limits) error {
	if len(m.raw) == 0 {
		return ErrInvalid
	}
	if _, err := newManifest(m.document.Identity, m.document.Members, limits); err != nil {
		return err
	}
	if envelopeSize(m) > limits.ArchiveBytes {
		return ErrLimit
	}
	return nil
}

func hasNull(fields map[string]json.RawMessage) bool {
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return true
		}
	}
	return false
}
