//go:build linux || darwin

package backupcredentials

import (
	"bytes"
	"encoding/json"
)

func (f Files) installArchive(receipt ArchiveReceipt, env []byte) error {
	if !receipt.Valid() {
		return ErrInvalid
	}
	metadata, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	return f.installMetadata(receipt.Plan.Scope.fileRef(), receipt.Plan.Version, metadata, env)
}

func (f Files) ArchiveReceipt(ref string, version uint64) (ArchiveReceipt, error) {
	var receipt ArchiveReceipt
	raw, err := f.receiptBytes(ref, version)
	if err != nil {
		return receipt, err
	}
	if json.Unmarshal(raw, &receipt) != nil || !receipt.Valid() || receipt.Plan.Scope.fileRef() != ref || receipt.Plan.Version != version {
		return ArchiveReceipt{}, ErrStorage
	}
	canonical, err := json.Marshal(receipt)
	if err != nil || !bytes.Equal(raw, canonical) {
		return ArchiveReceipt{}, ErrStorage
	}
	return receipt, nil
}
