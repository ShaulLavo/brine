package backupcredentials

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"time"
)

const ArchiveKind = "archive_credentials_renew"

// ArchiveScope is distinct from the active-replica scope. Its admission callback
// must load the retired archive and exact original binding/epoch under the host
// lock, check held fences and current destination authorization, and refuse any
// local deletion intent. Renewal never clears a fence or activates a replica.
type ArchiveScope struct {
	ArchiveID     string    `json:"archive_id"`
	Binding       string    `json:"binding"`
	Epoch         string    `json:"epoch"`
	CredentialRef string    `json:"credential_ref"`
	Destination   string    `json:"destination"`
	TargetHash    string    `json:"target_hash"`
	PolicyHash    string    `json:"policy_hash"`
	RetainUntil   time.Time `json:"retain_until"`
}

func (s ArchiveScope) Valid() bool {
	return idPattern.MatchString(s.ArchiveID) && idPattern.MatchString(s.Binding) && idPattern.MatchString(s.Epoch) && namePattern.MatchString(s.CredentialRef) && namePattern.MatchString(s.Destination) && hashPattern.MatchString(s.TargetHash) && hashPattern.MatchString(s.PolicyHash) && !s.RetainUntil.IsZero() && s.RetainUntil.Location() == time.UTC
}

// FileReference identifies only this archived binding/epoch's private namespace.
func (s ArchiveScope) FileReference() string { return s.fileRef() }

func (s ArchiveScope) fileRef() string {
	// Full-width base32 keeps the namespace within the existing filename grammar
	// without truncating the identity hash. Neither app names nor current epochs
	// select an archive credential namespace.
	raw, _ := json.Marshal(struct{ ArchiveID, Binding, Epoch, CredentialRef, Destination string }{s.ArchiveID, s.Binding, s.Epoch, s.CredentialRef, s.Destination})
	sum := sha256.Sum256(raw)
	return "archive-" + strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
}

type ArchivePlan struct {
	ID        string       `json:"plan_id"`
	Kind      string       `json:"kind"`
	Requester string       `json:"requester"`
	Scope     ArchiveScope `json:"scope"`
	Version   uint64       `json:"version"`
	ExpiresAt *time.Time   `json:"expires_at,omitempty"`
}

func (p ArchivePlan) identity() string {
	p.ID = ""
	raw, _ := json.Marshal(p)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func (p ArchivePlan) Valid() bool {
	return p.Kind == ArchiveKind && validRequester(p.Requester) && p.Scope.Valid() && p.Version > 0 && p.ID == p.identity() && (p.ExpiresAt == nil || p.ExpiresAt.Location() == time.UTC)
}

type ArchiveReceipt struct {
	Plan       ArchivePlan `json:"plan"`
	File       string      `json:"file"`
	ReceivedAt time.Time   `json:"received_at"`
}

func (r ArchiveReceipt) Valid() bool {
	return r.Plan.Valid() && !r.ReceivedAt.IsZero() && r.ReceivedAt.Before(r.Plan.Scope.RetainUntil) && (r.Plan.ExpiresAt == nil || r.ReceivedAt.Before(*r.Plan.ExpiresAt)) && r.File == fileName(r.Plan.Scope.fileRef(), r.Plan.Version, "env")
}
func (r ArchiveReceipt) Admit(now time.Time, budget time.Duration) error {
	if !r.Valid() || budget < 0 || now.Before(r.ReceivedAt) {
		return ErrInvalid
	}
	if !now.Add(budget).Before(r.Plan.Scope.RetainUntil) || r.Plan.ExpiresAt != nil && !now.Add(budget).Before(*r.Plan.ExpiresAt) {
		return ErrExpired
	}
	return nil
}

type ArchiveJournal interface {
	RecordArchiveCredentialPlan(context.Context, ArchivePlan) error
	LoadArchiveCredentialPlan(context.Context, string) (ArchivePlan, error)
	RecordArchiveCredentialReceipt(context.Context, ArchiveReceipt) error
}

// ArchiveService has no activation callback or replica process interface.
type ArchiveService struct {
	Requester string
	Journal   ArchiveJournal
	Files     Files
	Scope     func(context.Context, string, string, string) (ArchiveScope, error)
	Lock      func(context.Context) (func() error, error)
	Now       func() time.Time
}

func (s ArchiveService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s ArchiveService) locked(ctx context.Context, fn func() error) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.Scope == nil || s.Journal == nil || s.Lock == nil || !validRequester(s.Requester) {
		return ErrInvalid
	}
	unlock, err := s.Lock(ctx)
	if err != nil {
		return ErrStorage
	}
	defer func() { err = errors.Join(err, unlock()) }()
	return s.Files.locked(ctx, fn)
}
func (s ArchiveService) Plan(ctx context.Context, archiveID, binding, epoch string, expiry *time.Time) (out ArchivePlan, err error) {
	err = s.locked(ctx, func() error {
		scope, err := s.Scope(ctx, archiveID, binding, epoch)
		if err != nil {
			return err
		}
		if !scope.Valid() || scope.ArchiveID != archiveID || scope.Binding != binding || scope.Epoch != epoch {
			return ErrInvalid
		}
		if !s.now().Before(scope.RetainUntil) {
			return ErrExpired
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
		version, err := s.Files.Next(scope.fileRef())
		if err != nil || version == math.MaxUint64 {
			return ErrStorage
		}
		out = ArchivePlan{Kind: ArchiveKind, Requester: s.Requester, Scope: scope, Version: version, ExpiresAt: expiry}
		out.ID = out.identity()
		if err := s.Journal.RecordArchiveCredentialPlan(ctx, out); err != nil {
			return ErrStorage
		}
		return nil
	})
	return out, err
}
func (s ArchiveService) Deliver(ctx context.Context, id string, packet Packet) (out ArchiveReceipt, err error) {
	if packet.get == nil {
		return out, ErrInvalid
	}
	accessKey, secretKey, sessionToken := packet.get()
	if !validValue(accessKey) || !validValue(secretKey) || sessionToken != "" && !validValue(sessionToken) {
		return out, ErrInvalid
	}
	err = s.locked(ctx, func() error {
		plan, err := s.Journal.LoadArchiveCredentialPlan(ctx, id)
		if err != nil || !plan.Valid() || plan.ID != id || plan.Requester != s.Requester {
			return ErrInvalid
		}
		if (plan.ExpiresAt == nil) != (packet.expiresAt == nil) || plan.ExpiresAt != nil && !plan.ExpiresAt.Equal(*packet.expiresAt) {
			return ErrStale
		}
		scope, err := s.Scope(ctx, plan.Scope.ArchiveID, plan.Scope.Binding, plan.Scope.Epoch)
		if err != nil {
			return err
		}
		if scope != plan.Scope || !scope.Valid() {
			return ErrStale
		}
		ref := scope.fileRef()
		if existing, err := s.Files.ArchiveReceipt(ref, plan.Version); err == nil {
			if existing.Plan.ID != plan.ID || existing.Plan.Scope != scope {
				return ErrStale
			}
			if err := existing.Admit(s.now(), 0); err != nil {
				return err
			}
			saved, err := s.Files.Read(ref, plan.Version)
			if err != nil {
				return ErrStorage
			}
			same := slices.Equal(saved.Environment(), packet.Environment())
			saved.Clear()
			if !same {
				return ErrStale
			}
			if err := s.Journal.RecordArchiveCredentialReceipt(ctx, existing); err != nil {
				return ErrStorage
			}
			out = existing
			return nil
		}
		version, err := s.Files.Next(ref)
		if err != nil {
			return ErrStorage
		}
		if version != plan.Version {
			return ErrStale
		}
		out = ArchiveReceipt{Plan: plan, File: fileName(ref, version, "env"), ReceivedAt: s.now()}
		if err := out.Admit(s.now(), 0); err != nil {
			return err
		}
		env := packet.environment()
		defer clear(env)
		if err := s.Files.installArchive(out, env); err != nil {
			return ErrStorage
		}
		journalCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := s.Journal.RecordArchiveCredentialReceipt(journalCtx, out); err != nil {
			return ErrStorage
		}
		return nil
	})
	if err != nil {
		return ArchiveReceipt{}, err
	}
	return out, nil
}

func (s ArchiveService) DeliverScoped(ctx context.Context, archiveID, binding, epoch, id string, packet Packet) (ArchiveReceipt, error) {
	if s.Journal == nil || !idPattern.MatchString(archiveID) || !idPattern.MatchString(binding) || !idPattern.MatchString(epoch) || !hashPattern.MatchString(id) {
		return ArchiveReceipt{}, ErrInvalid
	}
	plan, err := s.Journal.LoadArchiveCredentialPlan(ctx, id)
	if err != nil || !plan.Valid() || plan.Scope.ArchiveID != archiveID || plan.Scope.Binding != binding || plan.Scope.Epoch != epoch {
		return ArchiveReceipt{}, ErrStale
	}
	return s.Deliver(ctx, id, packet)
}
