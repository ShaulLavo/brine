package ops

import (
	"regexp"
	"strconv"
)

type RestoreTaskInput struct {
	App      string `json:"app"`
	Database string `json:"database"`
	TXID     string `json:"txid"`
	Point    string `json:"point"`
}

var restorePointID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)

func (a RestoreTaskInput) Valid() bool {
	if !appName.MatchString(a.App) || a.Database != "" && !appName.MatchString(a.Database) || a.TXID != "" && a.Point != "" {
		return false
	}
	if a.Point != "" && !restorePointID.MatchString(a.Point) {
		return false
	}
	if a.TXID != "" {
		if len(a.TXID) > 16 {
			return false
		}
		for _, c := range a.TXID {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
		n, err := strconv.ParseUint(a.TXID, 16, 64)
		if err != nil || n == 0 {
			return false
		}
	}
	return true
}
