// Package planfile stores offline planning evidence. It has no host operations,
// approval transitions or control database, and never produces apply authority.
package planfile

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/target"
)

const SchemaVersion = 1
const MaxFileBytes = 16 << 20

type Metadata struct {
	CreatedAt   time.Time `json:"created_at"`
	ToolVersion string    `json:"tool_version"`
}

// Offline owns validated, immutable bytes. Applyable and Reason are constants,
// not caller-writable state. The zero value cannot be serialized or persisted.
type Offline struct {
	data []byte
	hash string
}

type inputs struct {
	Desired  policy.Desired  `json:"desired"`
	Snapshot target.Snapshot `json:"snapshot"`
	State    plan.BrineState `json:"brine_state"`
}

type envelope struct {
	SchemaVersion      int                        `json:"schema_version"`
	Plan               plan.Plan                  `json:"plan"`
	Hash               string                     `json:"hash"`
	Applyable          *bool                      `json:"applyable"`
	Reason             string                     `json:"reason"`
	SnapshotIdentity   target.Identity            `json:"snapshot_identity"`
	SnapshotGeneration target.Observation[uint64] `json:"snapshot_generation"`
	Inputs             inputs                     `json:"inputs"`
	Metadata           Metadata                   `json:"metadata"`
}

type IntegrityError struct{}

func (*IntegrityError) Error() string {
	return "offline plan content does not match its fingerprint or snapshot binding"
}

type SchemaError struct{ Version int }

func (e *SchemaError) Error() string {
	return fmt.Sprintf("unsupported offline plan schema version %d", e.Version)
}

type DecodeError struct{}

func (*DecodeError) Error() string { return "invalid offline plan file" }

type ConflictError struct{ Path string }

func (e *ConflictError) Error() string {
	return "different content already exists at offline plan path " + e.Path
}

// New computes a plan without I/O or a clock. Verification inputs are retained
// because the planner hashes the complete snapshot and committed release state,
// not just the visible plan. Metadata is deliberately outside that fingerprint.
func New(in plan.Input, metadata Metadata) (Offline, error) {
	if metadata.CreatedAt.IsZero() || metadata.ToolVersion == "" {
		return Offline{}, &DecodeError{}
	}
	p, err := plan.Build(in)
	if err != nil {
		return Offline{}, err
	}
	no := false
	e := envelope{SchemaVersion: SchemaVersion, Plan: p, Hash: p.Hash, Applyable: &no, Reason: "offline", SnapshotIdentity: p.Target, SnapshotGeneration: p.ObservedGeneration, Inputs: inputs{in.Desired, in.Snapshot, in.State}, Metadata: metadata}
	e.Metadata.CreatedAt = e.Metadata.CreatedAt.UTC()
	b, err := json.Marshal(e)
	if err != nil || len(b)+1 > MaxFileBytes {
		return Offline{}, &DecodeError{}
	}
	return Offline{data: append(b, '\n'), hash: p.Hash}, nil
}

func (p Offline) Applyable() bool    { return false }
func (p Offline) Reason() string     { return "offline" }
func (p Offline) Hash() string       { return p.hash }
func (p Offline) Filename() string   { return strings.TrimPrefix(p.Hash(), "sha256:") + ".plan.json" }
func (p Offline) Plan() plan.Plan    { return p.document().Plan }
func (p Offline) Metadata() Metadata { return p.document().Metadata }
func (p Offline) document() envelope { var e envelope; _ = json.Unmarshal(p.data, &e); return e }
func (p Offline) MarshalJSON() ([]byte, error) {
	if len(p.data) == 0 {
		return nil, &DecodeError{}
	}
	return bytes.Clone(p.data), nil
}

func Decode(b []byte) (Offline, error) {
	if len(b) > MaxFileBytes || uniqueFields(b) != nil {
		return Offline{}, &DecodeError{}
	}
	var e envelope
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&e); err != nil {
		return Offline{}, &DecodeError{}
	}
	if e.SchemaVersion != SchemaVersion {
		return Offline{}, &SchemaError{e.SchemaVersion}
	}
	if e.Plan.SchemaVersion != plan.SchemaVersion {
		return Offline{}, &SchemaError{e.Plan.SchemaVersion}
	}
	if e.Applyable == nil || *e.Applyable || e.Reason != "offline" || e.Metadata.CreatedAt.IsZero() || e.Metadata.ToolVersion == "" {
		return Offline{}, &DecodeError{}
	}
	computed, err := plan.Build(plan.Input{Desired: e.Inputs.Desired, Snapshot: e.Inputs.Snapshot, Image: e.Plan.Image, State: e.Inputs.State})
	if err != nil {
		return Offline{}, &IntegrityError{}
	}
	want, err := computed.CanonicalBytes()
	if err != nil {
		return Offline{}, &IntegrityError{}
	}
	got, err := e.Plan.CanonicalBytes()
	if err != nil {
		return Offline{}, &IntegrityError{}
	}
	if !bytes.Equal(want, got) || e.Hash != computed.Hash || e.SnapshotIdentity != computed.Target || !reflect.DeepEqual(e.SnapshotGeneration, computed.ObservedGeneration) {
		return Offline{}, &IntegrityError{}
	}
	e.Metadata.CreatedAt = e.Metadata.CreatedAt.UTC()
	data, err := json.Marshal(e)
	if err != nil {
		return Offline{}, &DecodeError{}
	}
	return Offline{data: append(data, '\n'), hash: computed.Hash}, nil
}

// Read additionally checks the content-addressed filename. File size is bounded
// before decoding, including for streams whose size is not reported by stat.
func Read(path string) (Offline, error) {
	f, err := os.Open(path)
	if err != nil {
		return Offline{}, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return Offline{}, err
	}
	p, err := Decode(b)
	if err != nil {
		return Offline{}, err
	}
	if filepath.Base(path) != p.Filename() {
		return Offline{}, &IntegrityError{}
	}
	return p, nil
}

type temporaryFile interface {
	io.Writer
	Sync() error
	Close() error
	Name() string
}
type writeOps struct {
	createTemp func(string, string) (temporaryFile, error)
	publish    func(string, string) error
}

// Write requires an existing directory. It fsyncs the owner-only temporary file,
// publishes without replacing a destination, then fsyncs the directory. A sync
// failure after publication returns an error; retrying identical bytes is safe.
func Write(dir string, p Offline) (string, error) {
	return writeWith(dir, p, writeOps{createTemp: func(dir, pattern string) (temporaryFile, error) { return os.CreateTemp(dir, pattern) }, publish: publish})
}
func writeWith(dir string, p Offline, ops writeOps) (string, error) {
	if len(p.data) == 0 {
		return "", &DecodeError{}
	}
	path := filepath.Join(dir, p.Filename())
	directory, err := os.Open(dir)
	if err != nil {
		return "", err
	}
	defer directory.Close()
	f, err := ops.createTemp(dir, ".brine-plan-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if n, err := f.Write(p.data); err != nil {
		_ = f.Close()
		return "", err
	} else if n != len(p.data) {
		_ = f.Close()
		return "", io.ErrShortWrite
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := ops.publish(f.Name(), path); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return "", err
		}
		info, statErr := os.Lstat(path)
		if statErr != nil {
			return "", statErr
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != int64(len(p.data)) {
			return "", &ConflictError{path}
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", readErr
		}
		if !bytes.Equal(existing, p.data) {
			return "", &ConflictError{path}
		}
	}
	if err := directory.Sync(); err != nil {
		return "", err
	}
	return path, nil
}

// encoding/json accepts duplicate keys. Detect them at every nesting level
// before decoding so an apparent offline marker cannot be shadowed later.
func uniqueFields(b []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	var value func() error
	value = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for decoder.More() {
				key, err := decoder.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return &DecodeError{}
				}
				seen[name] = true
				if err := value(); err != nil {
					return err
				}
			}
		case '[':
			for decoder.More() {
				if err := value(); err != nil {
					return err
				}
			}
		default:
			return &DecodeError{}
		}
		_, err = decoder.Token()
		return err
	}
	if err := value(); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		return &DecodeError{}
	}
	return nil
}
