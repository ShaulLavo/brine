//go:build !linux && !darwin

package inventory

import "os"

func protectedTool(os.FileInfo, bool) bool { return false }
func openTool(string) (*os.File, error)    { return nil, os.ErrPermission }
