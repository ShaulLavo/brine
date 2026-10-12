package restic

import (
	"context"
	"errors"
	"os"
	"time"
)

// AuthenticatedRestore comes only from exact encrypted download, full envelope
// checks and rooted extraction. It does not authorize native replay or mutation.
type AuthenticatedRestore struct {
	root       *os.Root
	parent     *os.Root
	name       string
	identity   os.FileInfo
	point      TransportPoint
	manifest   Manifest
	limits     Limits
	verifiedAt time.Time
}

func (r AuthenticatedRestore) Root() *os.Root        { return r.root }
func (r AuthenticatedRestore) Point() TransportPoint { return r.point }
func (r AuthenticatedRestore) Manifest() Manifest    { return r.manifest }
func (r AuthenticatedRestore) VerifiedAt() time.Time { return r.verifiedAt }

// MatchesWorkingCopy verifies a separate private copy against the complete
// authenticated envelope while preserving the original download.
func (r AuthenticatedRestore) MatchesWorkingCopy(ctx context.Context, parent *os.Root, name string, root *os.Root) bool {
	if parent == nil || root == nil || name != "working" || !r.Matches(ctx, r.parent, r.name, r.point, r.manifest) {
		return false
	}
	current, err := parent.Lstat(name)
	held, heldErr := root.Stat(".")
	if err != nil || heldErr != nil || !current.IsDir() || !os.SameFile(current, held) || os.SameFile(held, r.identity) {
		return false
	}
	return verifyMemberSet(ctx, root, held, r.manifest, r.limits) == nil
}

func (AuthenticatedRestore) MarshalJSON() ([]byte, error) {
	return nil, errors.New("authenticated restore proof cannot be serialized")
}

func (r AuthenticatedRestore) Matches(ctx context.Context, parent *os.Root, name string, point TransportPoint, manifest Manifest) bool {
	if ctx.Err() != nil || r.root == nil || r.parent == nil || r.parent != parent || r.name != name || r.identity == nil || r.point != point || r.manifest.SHA256() != manifest.SHA256() || r.verifiedAt.IsZero() || r.verifiedAt.Location() != time.UTC || r.verifiedAt.After(time.Now().UTC()) {
		return false
	}
	current, err := parent.Lstat(name)
	opened, openedErr := r.root.Stat(".")
	if err != nil || openedErr != nil || !current.IsDir() || !os.SameFile(current, r.identity) || !os.SameFile(opened, r.identity) {
		return false
	}
	return verifyMemberSet(ctx, r.root, opened, manifest, r.limits) == nil
}
