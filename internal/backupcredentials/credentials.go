// Package backupcredentials delivers externally issued S3 credentials without activating replication.
package backupcredentials

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/strictjson"
)

const PacketLimit = 32 << 10
const Kind = "backup_credentials_set"

var (
	ErrInvalid  = errors.New("invalid backup credential packet or scope")
	ErrStale    = errors.New("backup credential plan is stale")
	ErrExpired  = errors.New("backup credential lifetime is insufficient")
	ErrStorage  = errors.New("backup credential storage requires reconciliation")
	namePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	idPattern   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	hashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Packet struct {
	accessKey    string
	secretKey    string
	sessionToken string
	expiresAt    *time.Time
}

func (Packet) String() string               { return "[private backup credential packet]" }
func (Packet) GoString() string             { return "[private backup credential packet]" }
func (Packet) MarshalJSON() ([]byte, error) { return []byte(`{"private":true}`), nil }
func (p *Packet) Clear()                    { *p = Packet{} }
func (p Packet) Expiry() *time.Time {
	if p.expiresAt == nil {
		return nil
	}
	t := *p.expiresAt
	return &t
}

func ReadPacket(r io.Reader) (Packet, error) {
	if r == nil {
		return Packet{}, ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(r, PacketLimit+1))
	defer clear(raw)
	if err != nil || len(raw) > PacketLimit {
		return Packet{}, ErrInvalid
	}
	return DecodePacket(raw)
}
func DecodePacket(raw []byte) (Packet, error) {
	if len(raw) > PacketLimit {
		return Packet{}, ErrInvalid
	}
	fields, err := optionalObject(raw, []string{"access_key_id", "secret_access_key"}, "session_token", "expires_at")
	if err != nil {
		return Packet{}, ErrInvalid
	}
	p := Packet{}
	p.accessKey, err = strictjson.Value[string](fields["access_key_id"])
	if err != nil {
		return Packet{}, ErrInvalid
	}
	p.secretKey, err = strictjson.Value[string](fields["secret_access_key"])
	if err != nil {
		return Packet{}, ErrInvalid
	}
	if b, ok := fields["session_token"]; ok {
		p.sessionToken, err = strictjson.Value[string](b)
		if err != nil || p.sessionToken == "" {
			return Packet{}, ErrInvalid
		}
	}
	if b, ok := fields["expires_at"]; ok {
		var s string
		s, err = strictjson.Value[string](b)
		if err != nil || !strings.HasSuffix(s, "Z") {
			return Packet{}, ErrInvalid
		}
		t, e := time.Parse(time.RFC3339Nano, s)
		if e != nil {
			return Packet{}, ErrInvalid
		}
		p.expiresAt = &t
	}
	if !validValue(p.accessKey) || !validValue(p.secretKey) || p.sessionToken != "" && !validValue(p.sessionToken) {
		return Packet{}, ErrInvalid
	}
	return p, nil
}
func validValue(v string) bool {
	if len(v) == 0 || len(v) > 16<<10 {
		return false
	}
	for _, c := range []byte(v) {
		if c < 33 || c > 126 || c == '"' || c == '\'' || c == '\\' || c == '`' || c == '$' {
			return false
		}
	}
	return true
}
func (p Packet) environment() []byte {
	b := []byte("AWS_ACCESS_KEY_ID=" + p.accessKey + "\nAWS_SECRET_ACCESS_KEY=" + p.secretKey + "\n")
	if p.sessionToken != "" {
		b = append(b, []byte("AWS_SESSION_TOKEN="+p.sessionToken+"\n")...)
	}
	return b
}

type Scope struct {
	TargetHash    string `json:"target_hash"`
	App           string `json:"app"`
	CredentialRef string `json:"credential_ref"`
	Destination   string `json:"destination"`
	Binding       string `json:"binding"`
	Epoch         string `json:"epoch"`
	PolicyHash    string `json:"policy_hash"`
	Fenced        bool   `json:"fenced"`
}

func (s Scope) valid() bool {
	return hashPattern.MatchString(s.TargetHash) && namePattern.MatchString(s.App) && namePattern.MatchString(s.CredentialRef) && namePattern.MatchString(s.Destination) && idPattern.MatchString(s.Binding) && idPattern.MatchString(s.Epoch) && hashPattern.MatchString(s.PolicyHash) && !s.Fenced
}

type Plan struct {
	Requester string     `json:"requester"`
	Kind      string     `json:"kind"`
	ID        string     `json:"plan_id"`
	Scope     Scope      `json:"scope"`
	Version   uint64     `json:"version"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func (p Plan) identity() string {
	p.ID = ""
	b, _ := json.Marshal(p)
	h := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(h[:])
}
func (p Plan) Valid() bool {
	return p.Kind == Kind && validRequester(p.Requester) && p.Scope.valid() && p.Version > 0 && p.ID == p.identity() && (p.ExpiresAt == nil || p.ExpiresAt.Location() == time.UTC)
}

type Receipt struct {
	Requester  string     `json:"requester"`
	PlanID     string     `json:"plan_id"`
	Scope      Scope      `json:"scope"`
	Version    uint64     `json:"version"`
	File       string     `json:"file"`
	ReceivedAt time.Time  `json:"received_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	Activated  bool       `json:"activated"`
}

func (r Receipt) Valid() bool {
	p := Plan{Requester: r.Requester, Kind: Kind, ID: r.PlanID, Scope: r.Scope, Version: r.Version, ExpiresAt: r.ExpiresAt}
	return p.Valid() && r.File == fileName(r.Scope.CredentialRef, r.Version, "env") && !r.Activated && !r.ReceivedAt.IsZero()
}

type Health struct {
	AgeSeconds    int64      `json:"age_seconds"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Expired       bool       `json:"expired"`
	RenewalNeeded bool       `json:"renewal_needed"`
}

func (r Receipt) Health(now time.Time, budget time.Duration) Health {
	age := int64(now.Sub(r.ReceivedAt) / time.Second)
	if age < 0 {
		age = 0
	}
	h := Health{AgeSeconds: age, ExpiresAt: r.ExpiresAt}
	if r.ExpiresAt != nil {
		h.Expired = !now.Before(*r.ExpiresAt)
		h.RenewalNeeded = h.Expired || !now.Add(budget).Before(*r.ExpiresAt)
	}
	return h
}
func (r Receipt) Admit(now time.Time, budget time.Duration) error {
	if budget < 0 {
		return ErrInvalid
	}
	if r.Health(now, budget).RenewalNeeded {
		return ErrExpired
	}
	return nil
}

// Scope must re-read operator policy and committed binding/epoch state under Lock.
// Lock is the existing host mutation lock, not the replica lifetime lock.
type Journal interface {
	RecordPlan(context.Context, Plan) error
	LoadPlan(context.Context, string) (Plan, error)
	RecordReceipt(context.Context, Receipt) error
}
type Service struct {
	Requester string
	Journal   Journal
	Files     Files
	Scope     func(context.Context, string) (Scope, error)
	Lock      func(context.Context) (func(), error)
	Now       func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s Service) locked(ctx context.Context, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.Scope == nil || s.Journal == nil || !validRequester(s.Requester) {
		return ErrInvalid
	}
	if s.Lock != nil {
		release, err := s.Lock(ctx)
		if err != nil {
			return ErrStorage
		}
		defer release()
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn()
	}
	// Standalone delivery serializes private files. Host adapters also supply Lock.
	return s.Files.locked(ctx, fn)
}
func (s Service) Plan(ctx context.Context, app string, expiry *time.Time) (Plan, error) {
	var p Plan
	err := s.locked(ctx, func() error {
		scope, err := s.Scope(ctx, app)
		if err != nil || !scope.valid() || scope.App != app {
			return ErrInvalid
		}
		n, err := s.Files.Next(scope.CredentialRef)
		if err != nil {
			return ErrStorage
		}
		if n == math.MaxUint64 {
			return ErrStorage
		}
		if expiry != nil {
			if expiry.Location() != time.UTC {
				return ErrInvalid
			}
			e := *expiry
			expiry = &e
			if !s.now().Before(e) {
				return ErrExpired
			}
		}
		p = Plan{Requester: s.Requester, Kind: Kind, Scope: scope, Version: n, ExpiresAt: expiry}
		p.ID = p.identity()
		if err := s.Journal.RecordPlan(ctx, p); err != nil {
			return ErrStorage
		}
		return nil
	})
	return p, err
}
func (s Service) Deliver(ctx context.Context, p Plan, packet Packet) (Receipt, error) {
	var r Receipt
	if !p.Valid() || p.Requester != s.Requester || !validValue(packet.accessKey) || !validValue(packet.secretKey) || packet.sessionToken != "" && !validValue(packet.sessionToken) {
		return r, ErrInvalid
	}
	if (p.ExpiresAt == nil) != (packet.expiresAt == nil) || p.ExpiresAt != nil && !p.ExpiresAt.Equal(*packet.expiresAt) {
		return r, ErrStale
	}
	err := s.locked(ctx, func() error {
		scope, err := s.Scope(ctx, p.Scope.App)
		if err != nil || scope != p.Scope || !scope.valid() {
			return ErrStale
		}
		n, err := s.Files.Next(scope.CredentialRef)
		if err != nil {
			return ErrStorage
		}
		if n != p.Version {
			return ErrStale
		}
		r = Receipt{Requester: s.Requester, PlanID: p.ID, Scope: scope, Version: n, File: fileName(scope.CredentialRef, n, "env"), ReceivedAt: s.now(), ExpiresAt: packet.Expiry()}
		if err := r.Admit(r.ReceivedAt, 0); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		env := packet.environment()
		defer clear(env)
		if err := s.Files.install(r, env); err != nil {
			return ErrStorage
		}
		journalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.Journal.RecordReceipt(journalCtx, r); err != nil {
			return ErrStorage
		}
		return nil
	})
	if err != nil {
		return Receipt{}, err
	}
	return r, nil
}

func optionalObject(raw []byte, required []string, optional ...string) (map[string]json.RawMessage, error) {
	var values map[string]json.RawMessage
	if json.Unmarshal(raw, &values) != nil {
		return nil, ErrInvalid
	}
	allowed := map[string]bool{}
	for _, key := range required {
		allowed[key] = true
		if _, ok := values[key]; !ok {
			return nil, ErrInvalid
		}
	}
	for _, key := range optional {
		allowed[key] = true
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if !allowed[key] {
			return nil, ErrInvalid
		}
		keys = append(keys, key)
	}
	return strictjson.Object(raw, keys...)
}

// Credentials intentionally redact all generic formatting and serialization.
type Credentials = Packet

// Environment returns a fresh secret-bearing allowlisted environment for exec only.
func (p Packet) Environment() []string {
	env := []string{"AWS_ACCESS_KEY_ID=" + p.accessKey, "AWS_SECRET_ACCESS_KEY=" + p.secretKey}
	if p.sessionToken != "" {
		env = append(env, "AWS_SESSION_TOKEN="+p.sessionToken)
	}
	return env
}
func parseEnvironment(raw []byte) (Credentials, error) {
	if len(raw) == 0 || len(raw) > PacketLimit || raw[len(raw)-1] != '\n' {
		return Credentials{}, ErrInvalid
	}
	lines := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if len(lines) < 2 || len(lines) > 3 {
		return Credentials{}, ErrInvalid
	}
	values := map[string]string{}
	for _, line := range lines {
		key, value, ok := strings.Cut(line, "=")
		if !ok || !validValue(value) || values[key] != "" || (key != "AWS_ACCESS_KEY_ID" && key != "AWS_SECRET_ACCESS_KEY" && key != "AWS_SESSION_TOKEN") {
			return Credentials{}, ErrInvalid
		}
		values[key] = value
	}
	if values["AWS_ACCESS_KEY_ID"] == "" || values["AWS_SECRET_ACCESS_KEY"] == "" {
		return Credentials{}, ErrInvalid
	}
	return Credentials{accessKey: values["AWS_ACCESS_KEY_ID"], secretKey: values["AWS_SECRET_ACCESS_KEY"], sessionToken: values["AWS_SESSION_TOKEN"]}, nil
}

func (s Service) Set(ctx context.Context, app, planID string, p Packet) (Receipt, error) {
	if s.Journal == nil || !ValidPlanID(planID) {
		return Receipt{}, ErrInvalid
	}
	planned, err := s.Journal.LoadPlan(ctx, planID)
	if err != nil {
		return Receipt{}, ErrStale
	}
	if planned.ID != planID || planned.Scope.App != app {
		return Receipt{}, ErrStale
	}
	return s.Deliver(ctx, planned, p)
}

func DecodePlan(raw []byte) (Plan, error) {
	f, err := optionalObject(raw, []string{"kind", "plan_id", "scope", "version", "requester"}, "expires_at")
	if err != nil {
		return Plan{}, ErrInvalid
	}
	if _, err := strictjson.Object(f["scope"], "target_hash", "app", "credential_ref", "destination", "binding", "epoch", "policy_hash", "fenced"); err != nil {
		return Plan{}, ErrInvalid
	}
	var p Plan
	if json.Unmarshal(raw, &p) != nil || !p.Valid() {
		return Plan{}, ErrInvalid
	}
	return p, nil
}
func DecodeReceipt(raw []byte) (Receipt, error) {
	f, err := optionalObject(raw, []string{"plan_id", "scope", "version", "file", "received_at", "activated", "requester"}, "expires_at")
	if err != nil {
		return Receipt{}, ErrInvalid
	}
	if _, err := strictjson.Object(f["scope"], "target_hash", "app", "credential_ref", "destination", "binding", "epoch", "policy_hash", "fenced"); err != nil {
		return Receipt{}, ErrInvalid
	}
	var r Receipt
	if json.Unmarshal(raw, &r) != nil {
		return Receipt{}, ErrInvalid
	}
	if !r.Valid() {
		return Receipt{}, ErrInvalid
	}
	return r, nil
}

func ValidPlanID(id string) bool { return hashPattern.MatchString(id) }

func validRequester(requester string) bool {
	if len(requester) == 0 || len(requester) > 128 {
		return false
	}
	for _, c := range []byte(requester) {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ':') {
			return false
		}
	}
	return true
}
