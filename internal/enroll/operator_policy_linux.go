package enroll

import (
	"context"
	"errors"
	"os"
	"syscall"
)

type operatorPolicyFile interface {
	Read() ([]byte, error)
	Install(context.Context) error
	InstalledHash() (string, bool)
	Remove() error
	Retain() error
}

func operatorPolicyStep(f operatorPolicyFile) Step {
	return Step{Name: "operator-policy", Check: func(context.Context) (bool, error) {
		_, err := f.Read()
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}, Apply: func(ctx context.Context) error {
		_, err := f.Read()
		if err == nil {
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return f.Install(ctx)
	}, Undo: func(context.Context) error {
		data, err := f.Read()
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		installed, owned := f.InstalledHash()
		if !owned || hash(data) != installed {
			return f.Retain()
		}
		return f.Remove()
	}}
}

type diskOperatorPolicy struct{ h *host }

func (f diskOperatorPolicy) Read() ([]byte, error) {
	if err := protectedParents(operatorPolicyPath); err != nil {
		return nil, err
	}
	info, err := os.Lstat(operatorPolicyPath)
	if err != nil {
		return nil, err
	}
	if err := operatorPolicyMetadata(info); err != nil {
		return nil, err
	}
	return boundedRead(operatorPolicyPath)
}
func operatorPolicyMetadata(info os.FileInfo) error {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0644 || st.Uid != 0 || st.Nlink != 1 {
		return errors.New("operator policy must be a root-owned 0644 regular file without links")
	}
	return nil
}
func (f diskOperatorPolicy) Install(ctx context.Context) error {
	return f.h.file(ctx, operatorPolicyPath, []byte(defaultOperatorPolicy), 0644, false)
}
func (f diskOperatorPolicy) InstalledHash() (string, bool) {
	old, ok := f.h.r.Files[operatorPolicyPath]
	return old.Hash, ok && !old.Existed
}
func (f diskOperatorPolicy) Remove() error { return os.Remove(operatorPolicyPath) }
func (f diskOperatorPolicy) Retain() error {
	f.h.r.RetainedPolicy = true
	return f.h.Save(f.h.r.Journal)
}
