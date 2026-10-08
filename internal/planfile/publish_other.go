//go:build !linux

package planfile

import "os"

// A same-directory hard link is the portable no-replace publication primitive.
// Write removes the temporary name after publication on these platforms.
func publish(oldPath, newPath string) error { return os.Link(oldPath, newPath) }
