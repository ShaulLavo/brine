//go:build linux

package host

import (
	"fmt"
	"os"
	"path/filepath"
)

func e3Trace(root, stage string, err error) {
	f, openErr := os.OpenFile(filepath.Join(root, "e3-abandon-trace.txt"), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if openErr != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s %v\n", stage, err)
}
