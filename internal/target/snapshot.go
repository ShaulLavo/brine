package target

import (
	"encoding/base64"
	"fmt"
	"regexp"
)

const SchemaVersion = 1

type Status string

const (
	KnownStatus Status = "known"
	Unknown     Status = "unknown"
	Unsupported Status = "unsupported"
	Absent      Status = "absent"
)

// Observation distinguishes a measured zero from a fact inventory could not obtain.
// Only known observations carry a value. Absent is allowed only for optional artifacts.
type Observation[T any] struct {
	Status Status `json:"status"`
	Value  *T     `json:"value,omitempty"`
}

func Known[T any](value T) Observation[T] { return Observation[T]{Status: KnownStatus, Value: &value} }

type Port uint32

type Snapshot struct {
	SchemaVersion  int                          `json:"schema_version"`
	Identity       Identity                     `json:"identity"`
	OS             OS                           `json:"os"`
	Arch           string                       `json:"arch"`
	Versions       Versions                     `json:"versions"`
	CgroupV2       Observation[bool]            `json:"cgroup_v2"`
	Runner         Runner                       `json:"runner"`
	Generation     Observation[uint64]          `json:"generation"`
	CaddyConfig    Observation[CaddyConfigSet]  `json:"caddy_config"`
	Apps           Observation[[]App]           `json:"apps"`
	UsedPorts      Observation[[]Port]          `json:"used_ports"`
	LiveCaddyFiles Observation[[]LiveCaddyFile] `json:"live_caddy_files"`
	PortOwners     Observation[[]PortOwner]     `json:"port_owners"`
	FreeDiskBytes  Observation[uint64]          `json:"free_disk_bytes"`
}

type Identity struct {
	ID                 string `json:"id"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
}

type OS struct {
	ID      string `json:"id"`
	Version string `json:"version"`
}

type Versions struct {
	Systemd    Observation[string] `json:"systemd"`
	Podman     Observation[string] `json:"podman"`
	Passt      Observation[string] `json:"passt"`
	Caddy      Observation[string] `json:"caddy"`
	Litestream Observation[string] `json:"litestream"`
}

type Runner struct {
	User   Observation[string] `json:"user"`
	Linger Observation[bool]   `json:"linger"`
}

type App struct {
	Name              string                `json:"name"`
	Image             Observation[Image]    `json:"image"`
	AllocatedHostPort Observation[Port]     `json:"allocated_host_port"`
	QuadletUnits      Observation[[]Unit]   `json:"quadlet_units"`
	Secrets           Observation[[]Secret] `json:"secrets"`
}

// CaddyConfigSet describes the generation selected by the current symlink,
// including files that do not correspond to any recorded Brine app.
type CaddyConfigSet struct {
	Generation uint64      `json:"generation"`
	Files      []CaddyFile `json:"files"`
}

// LiveCaddyFile describes domains actually served by each root or imported file,
// including files outside Brine's generation directory. Name is a stable opaque
// file identifier. App is an observed app association, not replacement authority.
type LiveCaddyFile struct {
	Name    string                `json:"name"`
	App     string                `json:"app"`
	Domains Observation[[]string] `json:"domains"`
}

// PortOwner describes each bound listener. Multiple owners may share one port;
// an empty App is unrelated or unattributed, never affirmative Brine ownership.
type PortOwner struct {
	Port    Port   `json:"port"`
	App     string `json:"app"`
	Process string `json:"process"`
	Unit    string `json:"unit"`
}

type CaddyFile struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

type Image struct {
	Digest   string   `json:"digest"`
	Platform Platform `json:"platform"`
}

type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

type Unit struct {
	Name string `json:"name"`
	Hash string `json:"hash"`
}

// Secret.Name is the actual Podman name, not a logical app-spec reference.
// New releases bind immutable names brine-<app>-<ref>-v<n> and never values.
type Secret struct {
	Name string `json:"name"`
	ID   string `json:"id"`
}

type UnsupportedError struct {
	OS   OS
	Arch string
}

func (e *UnsupportedError) Error() string {
	return "unsupported target: Debian 13 on amd64 or arm64 required"
}
func (e *UnsupportedError) Code() string { return "unsupported" }

// Validate checks shape and reference-platform support. It does not claim that
// dependencies are installed or unknown observations are safe to apply.
func (s Snapshot) Validate() error {
	if err := s.validateShape(); err != nil {
		return err
	}
	if s.OS.ID != "debian" || s.OS.Version != "13" || (s.Arch != "amd64" && s.Arch != "arm64") {
		return &UnsupportedError{OS: s.OS, Arch: s.Arch}
	}
	return nil
}

var (
	tokenPattern     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)
	appPattern       = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	userPattern      = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	hashPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	versionPattern   = regexp.MustCompile(`^[!-~]{1,128}$`)
	caddyFilePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}\.caddy$`)
	unitPattern      = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}\.(container|volume|network|pod|kube|build|image|artifact)$`)
)

func checkPattern(pattern *regexp.Regexp, value string) error {
	if !pattern.MatchString(value) {
		return fmt.Errorf("invalid format")
	}
	return nil
}

func observe[T any](field string, o Observation[T], absent bool, check func(T) error) error {
	switch o.Status {
	case KnownStatus:
		if o.Value == nil {
			return fmt.Errorf("%s: known requires value", field)
		}
		if check != nil {
			if err := check(*o.Value); err != nil {
				return fmt.Errorf("%s: %w", field, err)
			}
		}
	case Unknown, Unsupported:
		if o.Value != nil {
			return fmt.Errorf("%s: unobserved fact has value", field)
		}
	case Absent:
		if !absent || o.Value != nil {
			return fmt.Errorf("%s: invalid absent observation", field)
		}
	default:
		return fmt.Errorf("%s: invalid observation status", field)
	}
	return nil
}

func validToken(value string) error { return checkPattern(tokenPattern, value) }
func validHash(value string) error  { return checkPattern(hashPattern, value) }
func validPort(value Port) error {
	if value == 0 || value > 65535 {
		return fmt.Errorf("port outside 1-65535")
	}
	return nil
}

func (s Snapshot) validateShape() error {
	if s.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version: expected %d", SchemaVersion)
	}
	if err := validToken(s.Identity.ID); err != nil {
		return fmt.Errorf("identity.id: %w", err)
	}
	fingerprint := s.Identity.HostKeyFingerprint
	if len(fingerprint) != 50 || fingerprint[:7] != "SHA256:" {
		return fmt.Errorf("identity.host_key_fingerprint: invalid SSH SHA256 fingerprint")
	}
	decoded, err := base64.RawStdEncoding.Strict().DecodeString(fingerprint[7:])
	if err != nil || len(decoded) != 32 {
		return fmt.Errorf("identity.host_key_fingerprint: invalid SSH SHA256 fingerprint")
	}
	for _, value := range []string{s.OS.ID, s.OS.Version, s.Arch} {
		if err := validToken(value); err != nil {
			return fmt.Errorf("os/arch: %w", err)
		}
	}
	for _, item := range []struct {
		name  string
		value Observation[string]
	}{
		{"versions.systemd", s.Versions.Systemd}, {"versions.podman", s.Versions.Podman}, {"versions.passt", s.Versions.Passt}, {"versions.caddy", s.Versions.Caddy}, {"versions.litestream", s.Versions.Litestream},
	} {
		if err := observe(item.name, item.value, true, func(v string) error { return checkPattern(versionPattern, v) }); err != nil {
			return err
		}
	}
	if err := observe("cgroup_v2", s.CgroupV2, false, nil); err != nil {
		return err
	}
	if err := observe("runner.user", s.Runner.User, true, func(v string) error { return checkPattern(userPattern, v) }); err != nil {
		return err
	}
	if err := observe("runner.linger", s.Runner.Linger, false, nil); err != nil {
		return err
	}
	if err := observe("generation", s.Generation, false, nil); err != nil {
		return err
	}
	if err := observe("caddy_config", s.CaddyConfig, true, func(config CaddyConfigSet) error {
		if config.Files == nil {
			return fmt.Errorf("use [] for a known empty Caddy file set")
		}
		seen := map[string]bool{}
		for _, file := range config.Files {
			if err := checkPattern(caddyFilePattern, file.Name); err != nil {
				return fmt.Errorf("file name: %w", err)
			}
			if err := validHash(file.Hash); err != nil {
				return fmt.Errorf("file hash: %w", err)
			}
			if seen[file.Name] {
				return fmt.Errorf("duplicate Caddy file name")
			}
			seen[file.Name] = true
		}
		return nil
	}); err != nil {
		return err
	}
	if err := observe("free_disk_bytes", s.FreeDiskBytes, false, nil); err != nil {
		return err
	}
	if err := observe("used_ports", s.UsedPorts, false, func(ports []Port) error {
		if ports == nil {
			return fmt.Errorf("use [] for known empty ports")
		}
		seen := map[Port]bool{}
		for _, port := range ports {
			if err := validPort(port); err != nil {
				return err
			}
			if seen[port] {
				return fmt.Errorf("duplicate port")
			}
			seen[port] = true
		}
		return nil
	}); err != nil {
		return err
	}

	if err := observe("live_caddy_files", s.LiveCaddyFiles, false, func(files []LiveCaddyFile) error {
		if files == nil {
			return fmt.Errorf("use [] for known empty live Caddy files")
		}
		seen := map[string]bool{}
		for _, file := range files {
			if validToken(file.Name) != nil || seen[file.Name] {
				return fmt.Errorf("invalid or duplicate live file identifier")
			}
			seen[file.Name] = true
			if file.App != "" && checkPattern(appPattern, file.App) != nil {
				return fmt.Errorf("invalid live file app")
			}
			if err := observe("domains", file.Domains, false, func(domains []string) error {
				if domains == nil {
					return fmt.Errorf("use [] for known empty domains")
				}
				seen := map[string]bool{}
				for _, domain := range domains {
					normalized, err := CanonicalDomain(domain)
					if err != nil {
						return err
					}
					if seen[normalized] {
						return fmt.Errorf("duplicate canonical domain")
					}
					seen[normalized] = true
				}
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := observe("port_owners", s.PortOwners, false, func(owners []PortOwner) error {
		if owners == nil {
			return fmt.Errorf("use [] for known empty port owners")
		}
		seen := map[PortOwner]bool{}
		for _, owner := range owners {
			if validPort(owner.Port) != nil || seen[owner] {
				return fmt.Errorf("invalid or duplicate port owner")
			}
			seen[owner] = true
			if owner.App != "" && checkPattern(appPattern, owner.App) != nil {
				return fmt.Errorf("invalid port owner app")
			}
			if (owner.Process != "" && validToken(owner.Process) != nil) || (owner.Unit != "" && validToken(owner.Unit) != nil) {
				return fmt.Errorf("invalid port owner process or unit")
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return observe("apps", s.Apps, false, func(apps []App) error {
		if apps == nil {
			return fmt.Errorf("use [] for known empty apps")
		}
		names := map[string]bool{}
		ports := map[Port]bool{}
		for _, app := range apps {
			if err := app.validate(); err != nil {
				return fmt.Errorf("app: %w", err)
			}
			if names[app.Name] {
				return fmt.Errorf("duplicate app name")
			}
			names[app.Name] = true
			if app.AllocatedHostPort.Value != nil {
				p := *app.AllocatedHostPort.Value
				if ports[p] {
					return fmt.Errorf("duplicate allocated app port")
				}
				ports[p] = true
			}
		}
		return nil
	})
}

func (a App) validate() error {
	if err := checkPattern(appPattern, a.Name); err != nil {
		return fmt.Errorf("name: %w", err)
	}
	if err := observe("image", a.Image, true, func(i Image) error {
		if err := validHash(i.Digest); err != nil {
			return err
		}
		if i.Platform.OS != "linux" || (i.Platform.Arch != "amd64" && i.Platform.Arch != "arm64") {
			return fmt.Errorf("unsupported image platform")
		}
		return nil
	}); err != nil {
		return err
	}
	if err := observe("allocated_host_port", a.AllocatedHostPort, true, validPort); err != nil {
		return err
	}
	if err := observe("quadlet_units", a.QuadletUnits, false, func(units []Unit) error {
		if units == nil {
			return fmt.Errorf("use [] for known empty units")
		}
		seen := map[string]bool{}
		for _, unit := range units {
			if err := checkPattern(unitPattern, unit.Name); err != nil {
				return err
			}
			if err := validHash(unit.Hash); err != nil {
				return err
			}
			if seen[unit.Name] {
				return fmt.Errorf("duplicate unit name")
			}
			seen[unit.Name] = true
		}
		return nil
	}); err != nil {
		return err
	}
	return observe("secrets", a.Secrets, false, func(secrets []Secret) error {
		if secrets == nil {
			return fmt.Errorf("use [] for known empty secrets")
		}
		names := map[string]bool{}
		ids := map[string]bool{}
		for _, secret := range secrets {
			if err := validToken(secret.Name); err != nil {
				return err
			}
			if err := validToken(secret.ID); err != nil {
				return err
			}
			if names[secret.Name] || ids[secret.ID] {
				return fmt.Errorf("duplicate secret name or ID")
			}
			names[secret.Name] = true
			ids[secret.ID] = true
		}
		return nil
	})
}
