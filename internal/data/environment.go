package data

import "strings"

func DatabaseEnvironmentName(name DatabaseName) string {
	return strings.ReplaceAll(strings.ToUpper(string(name)), "-", "_")
}
