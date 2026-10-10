// Package replication controls the pinned host Litestream service independently of app releases.
package replication

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

const ToolVersion = "0.5.17"
const Executable = "/opt/brine/litestream/" + ToolVersion + "/litestream"
const MaxConfigBytes = 32 * 1024

// Cadence is the effective spec/policy-admitted schedule. Each config has one
// database, so Litestream's global snapshot interval is that database's cadence.
type Cadence struct {
	SyncInterval     time.Duration
	SnapshotInterval time.Duration
}

func DefaultCadence() Cadence {
	return Cadence{SyncInterval: time.Minute, SnapshotInterval: 6 * time.Hour}
}

func (c Cadence) Validate() error {
	if c.SyncInterval < 10*time.Second || c.SyncInterval > time.Hour || c.SnapshotInterval < time.Hour || c.SnapshotInterval > 24*time.Hour {
		return ErrInvalid
	}
	return nil
}

var ErrInvalid = errors.New("replication: invalid binding or configuration")
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var tokenPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
var hashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Binding is an immutable destination/config projection. Store readers supply it
// only after checking database, epoch, destination and local-owner reservations.
type Binding struct {
	IncarnationID, DatabaseID, BindingID, EpochID string
	DBPath, SocketPath                            string
	Endpoint, Bucket, Prefix, Region              string
	ForcePathStyle                                bool
	Cadence                                       Cadence
}

func (b Binding) Validate() error {
	if err := b.Cadence.Validate(); err != nil {
		return err
	}
	for _, id := range []string{b.IncarnationID, b.DatabaseID, b.BindingID, b.EpochID} {
		if !idPattern.MatchString(id) {
			return ErrInvalid
		}
	}
	if !safePath(b.DBPath) || !safePath(b.SocketPath) || !strings.HasSuffix(b.SocketPath, "/replication/"+b.BindingID+"/control.sock") {
		return ErrInvalid
	}
	if !strings.HasSuffix(path.Dir(b.DBPath), "/apps/"+b.IncarnationID+"/databases/"+b.DatabaseID) || !tokenPattern.MatchString(path.Base(b.DBPath)) || len(path.Base(b.DBPath)) > 128 || len(b.SocketPath) > 107 {
		return ErrInvalid
	}
	for _, suffix := range []string{"-wal", "-shm", "-journal", "-litestream"} {
		if strings.HasSuffix(b.DBPath, suffix) {
			return ErrInvalid
		}
	}
	u, err := url.Parse(b.Endpoint)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(b.Endpoint, "$%\\\n\r\t ") {
		return ErrInvalid
	}
	if len(b.Bucket) < 3 || len(b.Bucket) > 63 || !tokenPattern.MatchString(b.Bucket) || len(b.Region) > 63 || !tokenPattern.MatchString(b.Region) {
		return ErrInvalid
	}
	suffix := "apps/" + b.IncarnationID + "/databases/" + b.DatabaseID + "/epochs/" + b.EpochID + "/"
	if !strings.HasSuffix(b.Prefix, suffix) || strings.HasPrefix(b.Prefix, "/") || path.Clean(b.Prefix)+"/" != b.Prefix || strings.ContainsAny(b.Prefix, "$%\\\n\r\t ") {
		return ErrInvalid
	}
	return nil
}
func safePath(s string) bool {
	return s != "/" && path.IsAbs(s) && path.Clean(s) == s && !strings.ContainsAny(s, "$%\\\n\r\t \x00")
}

// Config admits only the generated v0.5.17 topology. Credentials remain in the
// environment provider, never in this model or a URL constructor.
type Config struct {
	Snapshot    SnapshotConfig   `yaml:"snapshot"`
	Logging     LoggingConfig    `yaml:"logging"`
	Socket      SocketConfig     `yaml:"socket"`
	L0Retention string           `yaml:"l0-retention"`
	Retention   RetentionConfig  `yaml:"retention"`
	DBs         []DatabaseConfig `yaml:"dbs"`
}
type SnapshotConfig struct {
	Interval string `yaml:"interval"`
}
type LoggingConfig struct {
	Stderr *bool `yaml:"stderr"`
}
type SocketConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Path        string `yaml:"path"`
	Permissions uint32 `yaml:"permissions"`
}
type RetentionConfig struct {
	Enabled *bool `yaml:"enabled"`
}
type DatabaseConfig struct {
	Path               string        `yaml:"path"`
	MonitorInterval    string        `yaml:"monitor-interval"`
	CheckpointInterval string        `yaml:"checkpoint-interval"`
	BusyTimeout        string        `yaml:"busy-timeout"`
	Replica            ReplicaConfig `yaml:"replica"`
}
type ReplicaConfig struct {
	Type           string `yaml:"type"`
	Bucket         string `yaml:"bucket"`
	Path           string `yaml:"path"`
	Endpoint       string `yaml:"endpoint"`
	Region         string `yaml:"region"`
	ForcePathStyle *bool  `yaml:"force-path-style"`
	SyncInterval   string `yaml:"sync-interval"`
}

func RenderConfig(b Binding) ([]byte, error) {
	if err := b.Validate(); err != nil {
		return nil, err
	}
	disabled := false
	style := b.ForcePathStyle
	stderr := true
	c := Config{Snapshot: SnapshotConfig{Interval: b.Cadence.SnapshotInterval.String()}, Logging: LoggingConfig{Stderr: &stderr}, Socket: SocketConfig{Enabled: true, Path: b.SocketPath, Permissions: 0600}, L0Retention: "24h", Retention: RetentionConfig{Enabled: &disabled}, DBs: []DatabaseConfig{{Path: b.DBPath, MonitorInterval: "1s", CheckpointInterval: "1m", BusyTimeout: "5s", Replica: ReplicaConfig{Type: "s3", Bucket: b.Bucket, Path: b.Prefix, Endpoint: b.Endpoint, Region: b.Region, ForcePathStyle: &style, SyncInterval: b.Cadence.SyncInterval.String()}}}}
	raw, err := yaml.Marshal(c)
	if err != nil {
		return nil, ErrInvalid
	}
	if _, err = ParseConfig(raw, b); err != nil {
		return nil, err
	}
	return raw, nil
}
func ParseConfig(raw []byte, b Binding) (Config, error) {
	c, err := decodeConfig(raw)
	if err != nil || b.Validate() != nil || c.validate(b) != nil {
		return Config{}, ErrInvalid
	}
	return c, nil
}
func decodeConfig(raw []byte) (Config, error) {
	if len(raw) == 0 || len(raw) > MaxConfigBytes || bytes.ContainsAny(raw, "$\x00") {
		return Config{}, ErrInvalid
	}
	var node yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	if dec.Decode(&node) != nil || strictNode(&node) != nil {
		return Config{}, ErrInvalid
	}
	var extra yaml.Node
	if dec.Decode(&extra) != io.EOF {
		return Config{}, ErrInvalid
	}
	var c Config
	typed := yaml.NewDecoder(bytes.NewReader(raw))
	typed.KnownFields(true)
	if typed.Decode(&c) != nil {
		return Config{}, ErrInvalid
	}
	return c, nil
}
func strictNode(n *yaml.Node) error {
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return ErrInvalid
	}
	if n.Kind == yaml.MappingNode {
		keys := map[string]bool{}
		for i := 0; i < len(n.Content); i += 2 {
			k := n.Content[i]
			if k.Kind != yaml.ScalarNode || k.Tag != "!!str" || keys[k.Value] {
				return ErrInvalid
			}
			keys[k.Value] = true
		}
	}
	for _, child := range n.Content {
		if err := strictNode(child); err != nil {
			return err
		}
	}
	return nil
}
func (c Config) validate(b Binding) error {
	if c.Logging.Stderr == nil || !*c.Logging.Stderr || len(c.DBs) != 1 || !c.Socket.Enabled || c.Socket.Path != b.SocketPath || c.Socket.Permissions != 0600 || c.Retention.Enabled == nil || *c.Retention.Enabled {
		return ErrInvalid
	}
	d := c.DBs[0]
	r := d.Replica
	syncInterval, syncErr := time.ParseDuration(r.SyncInterval)
	snapshotInterval, snapshotErr := time.ParseDuration(c.Snapshot.Interval)
	if syncErr != nil || snapshotErr != nil || syncInterval != b.Cadence.SyncInterval || snapshotInterval != b.Cadence.SnapshotInterval {
		return ErrInvalid
	}
	if d.Path != b.DBPath || r.Type != "s3" || r.Bucket != b.Bucket || r.Path != b.Prefix || r.Endpoint != b.Endpoint || r.Region != b.Region || r.ForcePathStyle == nil || *r.ForcePathStyle != b.ForcePathStyle {
		return ErrInvalid
	}
	for _, value := range []string{c.L0Retention, d.MonitorInterval, d.CheckpointInterval, d.BusyTimeout, r.SyncInterval} {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return ErrInvalid
		}
	}
	return nil
}
func ConfigHash(raw []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) }
func ServiceName(bindingID string) (string, error) {
	if !idPattern.MatchString(bindingID) {
		return "", ErrInvalid
	}
	return "brine-litestream-" + bindingID + ".service", nil
}
