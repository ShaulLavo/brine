package enroll

import (
	"strings"
	"testing"
)

func TestForcedSSHRefusesEnvironmentPatternsAndOverrides(t *testing.T) {
	for _, v := range []string{"yes", "LANG*", "no LANG"} {
		if checkGlobalSSH("permituserenvironment "+v+"\n") == nil {
			t.Fatalf("accepted %q", v)
		}
	}
	if checkGlobalSSH("permituserenvironment no\n") != nil {
		t.Fatal("safe global refused")
	}
	for _, v := range []string{".ssh/authorized_keys", "none"} {
		if checkForcedSSH("authorizedkeysfile "+v+"\n") == nil {
			t.Fatal("alternate source accepted")
		}
	}
	if strings.Contains(sshPolicy, "PermitUserEnvironment") {
		t.Fatal("global-only directive in Match")
	}
}
