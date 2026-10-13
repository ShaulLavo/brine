//go:build linux

package store

import (
	"errors"
	"os"

	"github.com/ShaulLavo/brine/internal/data"
)

func checkPublicationWorkingPath(parent, root *os.Root, identity os.FileInfo, physicalPath string) error {
	if parent == nil || root == nil || identity == nil || !data.ValidRoot(physicalPath) {
		return ErrConflict
	}
	held, err := root.Stat(".")
	child, childErr := parent.Lstat("working")
	named, namedErr := os.Lstat(physicalPath)
	proof, proofErr := data.InspectRoot(physicalPath)
	heldID, heldErr := publicationHeldRootIdentity(root)
	if err != nil || childErr != nil || namedErr != nil || proofErr != nil || heldErr != nil || !os.SameFile(held, identity) || !os.SameFile(child, identity) || !os.SameFile(named, identity) {
		return errors.Join(ErrConflict, err, childErr, namedErr, proofErr, heldErr)
	}
	if proof.Device == 0 || proof.Inode == 0 || heldID != (data.EngineDirectoryIdentity{Device: proof.Device, Inode: proof.Inode}) {
		return ErrConflict
	}
	return nil
}
