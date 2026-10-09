// Package transport connects to a restricted dispatcher through system OpenSSH.
package transport

import (
	"encoding/base64"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/ShaulLavo/brine/internal/result"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const TargetLimit = 16 << 10

type Target struct {
	Name          string `json:"name"`
	Destination   string `json:"destination"`
	IdentityPath  string `json:"identity_path"`
	PinnedHostKey string `json:"pinned_host_key"`
}

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var userPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
var destinationPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]{0,252}$`)
var pathPattern = regexp.MustCompile(`^/[A-Za-z0-9_./-]+$`)

func safePath(path string) bool {
	return filepath.IsAbs(path) && pathPattern.MatchString(path) && filepath.Clean(path) == path
}

func (t Target) Validate() error {
	invalid := func() error { return result.New(result.TransportInvalidTarget, nil) }
	if !namePattern.MatchString(t.Name) || !safePath(t.IdentityPath) {
		return invalid()
	}
	parts := strings.Split(t.Destination, "@")
	host := t.Destination
	if len(parts) == 2 {
		if parts[0] == "root" || !userPattern.MatchString(parts[0]) {
			return invalid()
		}
		host = parts[1]
	} else if len(parts) != 1 {
		return invalid()
	}
	if !destinationPattern.MatchString(host) {
		return invalid()
	}
	key := strings.Split(t.PinnedHostKey, " ")
	if len(key) != 2 || key[0] != "ssh-ed25519" {
		return invalid()
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(key[1])
	if err != nil || len(blob) != 51 || binary.BigEndian.Uint32(blob[:4]) != 11 || string(blob[4:15]) != "ssh-ed25519" || binary.BigEndian.Uint32(blob[15:19]) != 32 {
		return invalid()
	}
	if base64.StdEncoding.EncodeToString(blob) != key[1] {
		return invalid()
	}
	return nil
}

func DecodeTarget(data []byte) (Target, error) {
	var t Target
	fail := func() (Target, error) { return Target{}, result.New(result.TransportInvalidTarget, nil) }
	if len(data) > TargetLimit {
		return fail()
	}
	f, err := strictjson.Object(data, "name", "destination", "identity_path", "pinned_host_key")
	if err != nil {
		return fail()
	}
	t.Name, err = strictjson.Value[string](f["name"])
	if err != nil {
		return fail()
	}
	t.Destination, err = strictjson.Value[string](f["destination"])
	if err != nil {
		return fail()
	}
	t.IdentityPath, err = strictjson.Value[string](f["identity_path"])
	if err != nil {
		return fail()
	}
	t.PinnedHostKey, err = strictjson.Value[string](f["pinned_host_key"])
	if err != nil {
		return fail()
	}
	if err := t.Validate(); err != nil {
		return Target{}, err
	}
	return t, nil
}

func LoadTarget(path string) (Target, error) {
	file, err := os.Open(path)
	if err != nil {
		return Target{}, result.New(result.TransportInvalidTarget, err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, TargetLimit+1))
	if err != nil {
		return Target{}, result.New(result.TransportInvalidTarget, err)
	}
	return DecodeTarget(data)
}
