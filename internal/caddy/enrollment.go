package caddy

import (
	"bytes"
	"errors"
	"strings"
)

// ValidateInventoryRoot applies D4's import execution-context restrictions.
func ValidateInventoryRoot(main []byte) error {
	_, err := candidateRoot(main, "/etc/caddy/brine", "gen-0")
	return err
}

func ValidateInventoryFile(data []byte) error {
	if bytes.Contains(data, []byte("{$")) {
		return errors.New("caddy: imported environment substitution is unsupported")
	}
	tokens, err := rootTokens(data)
	if err != nil {
		return err
	}
	for i, token := range tokens {
		lineStart := i == 0 || tokens[i-1].line+strings.Count(tokens[i-1].text, "\n") < token.line
		blockStart := i > 0 && tokens[i-1].text == "{" && tokens[i-1].quote == 0
		if token.text == "import" && (lineStart || blockStart) {
			return errors.New("caddy: imports in imported files are unsupported")
		}
	}
	return nil
}

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
