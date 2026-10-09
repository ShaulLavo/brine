package caddy

import (
	"bytes"
	"errors"
)

// EnrollmentRoot adds the fixed import and applies the same source restrictions
// as candidate validation. Existing Brine imports are not silently adopted.
func EnrollmentRoot(main []byte) ([]byte, error) {
	line := []byte("import /etc/caddy/brine/current/*.caddy\n")
	if bytes.Contains(main, line) {
		return nil, errors.New("caddy: preexisting Brine import refused")
	}
	next := append(append(append([]byte(nil), main...), '\n'), line...)
	if _, err := candidateRoot(next, "/etc/caddy/brine", "gen-0"); err != nil {
		return nil, err
	}
	return next, nil
}
