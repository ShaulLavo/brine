// Package runnerstorage defines the protected rootless storage dependency.
package runnerstorage

import (
	"encoding/json"
	"errors"
	"strings"
)

const Path = "/etc/brine/runner-storage.conf"
const Environment = "CONTAINERS_STORAGE_CONF=" + Path
const FusePath = "/usr/bin/fuse-overlayfs"
const Config = "[storage]\ndriver = \"overlay\"\n\n[storage.options.overlay]\nmount_program = \"/usr/bin/fuse-overlayfs\"\n"

var ErrProfile = errors.New("protected runner storage unavailable")

func ValidateVersion(raw string) error {
	if len(raw) > 4096 {
		return ErrProfile
	}
	for _, line := range strings.Split(raw, "\n") {
		if line == "fuse-overlayfs: version 1.13-dev" {
			return nil
		}
	}
	return ErrProfile
}

func ValidateInfo(raw []byte) error {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return ErrProfile
	}
	var v struct {
		Store struct {
			Driver  string                     `json:"graphDriverName"`
			Status  map[string]string          `json:"graphStatus"`
			Options map[string]json.RawMessage `json:"graphOptions"`
		} `json:"store"`
	}
	if json.Unmarshal(raw, &v) != nil || v.Store.Driver != "overlay" || v.Store.Status["Supports shifting"] != "true" {
		return ErrProfile
	}
	if len(v.Store.Options) != 1 {
		return ErrProfile
	}
	var mount struct {
		Executable string `json:"Executable"`
		Version    string `json:"Version"`
	}
	if json.Unmarshal(v.Store.Options["overlay.mount_program"], &mount) != nil || mount.Executable != FusePath || ValidateVersion(mount.Version) != nil {
		return ErrProfile
	}
	return nil
}
