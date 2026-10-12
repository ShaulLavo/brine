package plan

import "testing"

func TestVersionLineOptionalPrefixPreservesQualifiedBoundary(t *testing.T) {
	for _, tc := range []struct {
		value, baseline string
		accepted        bool
	}{
		{"v2.10.2", "2.10.2", true},
		{"2.10.2", "2.10.2", true},
		{"v2.10.2-1", "2.10.2", true},
		{"v2.10.2+build", "2.10.2", true},
		{"v2.10.2~rc1", "2.10.2", true},
		{"v5.4.2", "5.4", true},
		{"v257.13", "257", true},
		{"v2.6.2", "2.10.2", false},
		{"v2.10.3", "2.10.2", false},
		{"v2.10.20", "2.10.2", false},
		{"v2.10.2junk", "2.10.2", false},
		{"vv2.10.2", "2.10.2", false},
		{"V2.10.2", "2.10.2", false},
		{" v2.10.2", "2.10.2", false},
		{"v2.10.2 h1:checksum", "2.10.2", false},
		{"", "2.10.2", false},
	} {
		t.Run(tc.value+"/"+tc.baseline, func(t *testing.T) {
			if got := versionLine(tc.value, tc.baseline); got != tc.accepted {
				t.Fatalf("version admission=%t, want %t", got, tc.accepted)
			}
		})
	}
}
