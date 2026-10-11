//go:build linux

package data

import (
	"context"
	"errors"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

// InspectArchiveDeletion observes an exact archived inode or affirmative absence.
// Absence is evidence, not permission: the caller must require a matching durable
// tree-deletion intent before adopting it as a successful effect.
func InspectArchiveDeletion(t ArchiveTree, inc AppIncarnationID, id ArchiveID) (present bool, err error) {
	err = withArchiveDeletion(t, inc, id, func(_ []*os.File, bundle *os.File) error { present = bundle != nil; return nil })
	return present, err
}

// CaptureArchiveDeletionParent records the exact archives namespace while the
// bundle is still present. Retention/deletion plans retain this identity.
func CaptureArchiveDeletionParent(t ArchiveTree, inc AppIncarnationID, id ArchiveID) (out ArchiveDeletionParent, err error) {
	err = withArchiveDeletion(t, inc, id, func(parents []*os.File, bundle *os.File) error {
		if bundle == nil {
			return ErrInvalid
		}
		var stat unix.Stat_t
		// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
		if err := unix.Fstat(int(parents[2].Fd()), &stat); err != nil {
			return err
		}
		out = ArchiveDeletionParent{Root: t.Root, Device: uint64(stat.Dev), Inode: stat.Ino}
		return nil
	})
	return out, err
}
func archiveDeletionParentMatches(t ArchiveTree, parent *os.File, evidence ArchiveDeletionParent) error {
	if !evidence.Valid(t) {
		return ErrInvalid
	}
	var stat unix.Stat_t
	// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
	if unix.Fstat(int(parent.Fd()), &stat) != nil || uint64(stat.Dev) != evidence.Device || stat.Ino != evidence.Inode {
		return ErrInvalid
	}
	return nil
}
func InspectArchiveDeletionParent(t ArchiveTree, inc AppIncarnationID, id ArchiveID, evidence ArchiveDeletionParent) (present bool, err error) {
	err = withArchiveDeletion(t, inc, id, func(parents []*os.File, bundle *os.File) error {
		present = bundle != nil
		return archiveDeletionParentMatches(t, parents[2], evidence)
	})
	return present, err
}

// DeleteArchiveTree never accepts an application name or alternate local path.
// Only the immutable tree inode is traversed, through no-follow pinned handles.
func DeleteArchiveTree(ctx context.Context, t ArchiveTree, inc AppIncarnationID, id ArchiveID, evidence ArchiveDeletionParent, adopt bool) error {
	return withArchiveDeletion(t, inc, id, func(parents []*os.File, bundle *os.File) error {
		if err := archiveDeletionParentMatches(t, parents[2], evidence); err != nil {
			return err
		}
		if bundle == nil {
			if !adopt {
				return ErrInvalid
			}
		} else {
			count := 0
			if err := archiveDeleteContents(ctx, bundle, t.RootDevice, false, 0, &count); err != nil {
				return err
			}
			count = 0
			if _, err := bundle.Seek(0, 0); err != nil {
				return err
			}
			if err := archiveDeleteContents(ctx, bundle, t.RootDevice, true, 0, &count); err != nil {
				return err
			}
			var current unix.Stat_t
			// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
			if err := unix.Fstatat(int(parents[2].Fd()), string(id), &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || current.Mode&unix.S_IFMT != unix.S_IFDIR || uint64(current.Dev) != t.SourceDevice || current.Ino != t.SourceInode {
				return ErrInvalid
			}
			// Remove only the now-empty recorded leaf, never recursively by namespace.
			// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
			if err := unix.Unlinkat(int(parents[2].Fd()), string(id), unix.AT_REMOVEDIR); err != nil {
				return err
			}
		}
		for _, parent := range parents {
			if err := parent.Sync(); err != nil {
				return err
			}
		}
		return nil
	})
}

func withArchiveDeletion(t ArchiveTree, inc AppIncarnationID, id ArchiveID, work func([]*os.File, *os.File) error) (err error) {
	if !t.Valid(inc, id) || verifyRootAncestors(string(t.Root)) != nil {
		return ErrInvalid
	}
	fd, err := unix.Open(string(t.Root), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	root := os.NewFile(uintptr(fd), string(t.Root))
	files := []*os.File{root}
	defer func() {
		for _, file := range files {
			err = errors.Join(err, file.Close())
		}
	}()
	var rootStat unix.Stat_t
	if err = unix.Fstat(fd, &rootStat); err != nil || !archivePrivateDirectory(rootStat, t.RootDevice) || rootStat.Ino != t.RootInode {
		return ErrInvalid
	}
	for _, name := range []string{"apps", "archives"} {
		childFD, openErr := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return openErr
		}
		file := os.NewFile(uintptr(childFD), name)
		files = append(files, file)
		var stat unix.Stat_t
		if err = unix.Fstat(childFD, &stat); err != nil || !archivePrivateDirectory(stat, t.RootDevice) {
			return ErrInvalid
		}
	}
	var source unix.Stat_t
	// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
	if err = unix.Fstatat(int(files[1].Fd()), string(inc), &source, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return ErrInvalid
	}
	// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
	bundleFD, err := unix.Openat(int(files[2].Fd()), string(id), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	var bundle *os.File
	if err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	if err == nil {
		bundle = os.NewFile(uintptr(bundleFD), string(id))
		files = append(files, bundle)
		var stat unix.Stat_t
		if err = unix.Fstat(bundleFD, &stat); err != nil || !archivePrivateDirectory(stat, t.SourceDevice) || stat.Ino != t.SourceInode {
			return ErrInvalid
		}
	}
	check := func() error {
		device, inode, identityErr := directoryIdentity(string(t.Root))
		if identityErr != nil || device != t.RootDevice || inode != t.RootInode {
			return ErrInvalid
		}
		for _, parent := range files[1:3] {
			var pinned, entry unix.Stat_t
			// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
			if unix.Fstat(int(parent.Fd()), &pinned) != nil || unix.Fstatat(fd, parent.Name(), &entry, unix.AT_SYMLINK_NOFOLLOW) != nil || entry.Mode&unix.S_IFMT != unix.S_IFDIR || entry.Dev != pinned.Dev || entry.Ino != pinned.Ino {
				return ErrInvalid
			}
		}
		return nil
	}
	if err = check(); err != nil {
		return err
	}
	if err = work(files[:3], bundle); err != nil {
		return err
	}
	return check()
}
func archivePrivateDirectory(stat unix.Stat_t, device uint64) bool {
	// #nosec G115 -- The OS effective UID is nonnegative and fits the platform UID type.
	return stat.Mode&unix.S_IFMT == unix.S_IFDIR && stat.Mode&07777 == 0700 && stat.Uid == uint32(os.Geteuid()) && uint64(stat.Dev) == device
}
func archiveDeleteContents(ctx context.Context, directory *os.File, device uint64, remove bool, depth int, count *int) (err error) {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if depth > 32 {
		return ErrInvalid
	}
	// Bound entries before any effects; a retained tree is never followed through
	// symlinks, special files, foreign devices, hard links or foreign ownership.
	entries, err := directory.ReadDir(100001)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	for _, entry := range entries {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		*count++
		if *count > 100000 {
			return ErrInvalid
		}
		var stat unix.Stat_t
		// #nosec G115 -- Valid OS file descriptors fit the syscall descriptor type.
		fd := int(directory.Fd())
		if err = unix.Fstatat(fd, entry.Name(), &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			return err
		}
		// #nosec G115 -- The OS effective UID is nonnegative and fits the platform UID type.
		if uint64(stat.Dev) != device || stat.Uid != uint32(os.Geteuid()) {
			return ErrInvalid
		}
		switch stat.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			if !archivePrivateDirectory(stat, device) {
				return ErrInvalid
			}
			childFD, openErr := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if openErr != nil {
				return openErr
			}
			child := os.NewFile(uintptr(childFD), entry.Name())
			var pinned unix.Stat_t
			if err = unix.Fstat(childFD, &pinned); err != nil || pinned.Dev != stat.Dev || pinned.Ino != stat.Ino {
				return errors.Join(ErrInvalid, child.Close())
			}
			walkErr := archiveDeleteContents(ctx, child, device, remove, depth+1, count)
			if err = errors.Join(walkErr, child.Close()); err != nil {
				return err
			}
			if remove {
				var current unix.Stat_t
				if err = unix.Fstatat(fd, entry.Name(), &current, unix.AT_SYMLINK_NOFOLLOW); err != nil || current.Dev != stat.Dev || current.Ino != stat.Ino {
					return ErrInvalid
				}
				if err = unix.Unlinkat(fd, entry.Name(), unix.AT_REMOVEDIR); err != nil {
					return err
				}
			}
		case unix.S_IFREG:
			if stat.Mode&07777 != 0600 || stat.Nlink != 1 {
				return ErrInvalid
			}
			if remove {
				if err = unix.Unlinkat(fd, entry.Name(), 0); err != nil {
					return err
				}
			}
		default:
			return ErrInvalid
		}
	}
	if remove {
		return directory.Sync()
	}
	return nil
}
