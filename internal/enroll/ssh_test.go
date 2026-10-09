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

func TestSSHEnvironmentRefusesLoaderAndShellInputs(t *testing.T) {
	for _, suffix := range []string{"acceptenv *\n", "acceptenv LD_*\n", "acceptenv LANG LC_* LD_PRELOAD\n", "acceptenv LC_?\n", "setenv LD_PRELOAD=/fixture\n", "setenv BASH_ENV=/fixture\n", "setenv ENV=/fixture\n", "setenv PATH=/fixture\n", "setenv GCONV_PATH=/fixture\n", "setenv PYTHONPATH=/fixture\n"} {
		if err := checkGlobalSSH("permituserenvironment no\n" + suffix); err == nil {
			t.Fatalf("unsafe SSH environment accepted: %q", suffix)
		}
	}
}
