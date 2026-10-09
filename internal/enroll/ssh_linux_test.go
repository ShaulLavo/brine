package enroll

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/inventory"
	"github.com/ShaulLavo/brine/internal/localexec"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHPolicyAgainstRealOpenSSH(t *testing.T) {
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("OpenSSH server unavailable")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("keygen unavailable")
	}
	d := t.TempDir()
	key := filepath.Join(d, "hostkey")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v %s", err, out)
	}
	policy := filepath.Join(d, "policy.conf")
	if err := os.WriteFile(policy, []byte(sshPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	for _, branch := range []string{"Match Address 192.0.2.0/24\n AuthorizedKeysFile=.local/keys\n AuthorizedKeysCommand=/fixture\n", "Match User brine\n AuthorizedKeysFile=.config/keys\n AuthorizedKeysCommand=/fixture\n"} {
		included := filepath.Join(d, "later.conf")
		if err := os.WriteFile(included, []byte(branch), 0600); err != nil {
			t.Fatal(err)
		}
		main := []byte("HostKey " + key + "\nUsePAM yes\nPermitUserEnvironment no\nInclude=" + included + "\n")
		path := filepath.Join(d, "main.conf")
		if err := os.WriteFile(path, sshCandidate(main, policy), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(sshd, "-t", "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("sshd validation: %v %s", err, out)
		}
		original := filepath.Join(d, "original.conf")
		if err := os.WriteFile(original, main, 0600); err != nil {
			t.Fatal(err)
		}
		other := "user=ordinaryfixture,host=remote.example,addr=192.0.2.1,laddr=198.51.100.1,lport=22"
		before, err := exec.Command(sshd, "-T", "-f", original, "-C", other).Output()
		if err != nil {
			t.Fatal(err)
		}
		after, err := exec.Command(sshd, "-T", "-f", path, "-C", other).Output()
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("policy changed other-user settings")
		}
		for _, c := range sshConnections {
			out, err := exec.Command(sshd, "-T", "-f", path, "-C", c).Output()
			if err != nil {
				t.Fatal(err)
			}
			if err := checkForcedSSH(string(out)); err != nil {
				t.Fatalf("connection %s: %v", c, err)
			}
		}
	}
}
func TestSSHValidationAndReloadRestoration(t *testing.T) {
	for _, stage := range []string{"validation", "promotion", "reload", "unknown"} {
		t.Run(stage, func(t *testing.T) {
			promoted, restored, reloads, pending := false, false, 0, false
			boom := errors.New("failure")
			err := promoteSSH(context.Background(), func(context.Context) error {
				if stage == "validation" {
					return boom
				}
				return nil
			}, func() error {
				promoted = true
				if stage == "promotion" {
					return boom
				}
				return nil
			}, func(context.Context) error {
				reloads++
				if reloads == 1 && stage != "promotion" {
					if stage == "unknown" {
						return &localexec.Error{Kind: localexec.UnknownOutcome}
					}
					return boom
				}
				return nil
			}, func() error { restored = true; return nil }, func(v bool) error { pending = v; return nil })
			if err == nil {
				t.Fatal("failure lost")
			}
			if stage == "validation" && (promoted || restored || reloads != 0 || pending) {
				t.Fatal("invalid configuration touched live SSH")
			}
			if stage == "unknown" {
				if restored || reloads != 1 || !pending {
					t.Fatal("unknown outcome blindly retried")
				}
			} else if stage != "validation" && (!restored || pending || reloads == 0) {
				t.Fatal("original not restored and reloaded")
			}
		})
	}
}
func TestForcedPolicyMissingAndDuplicateFields(t *testing.T) {
	if checkForcedSSH("permituserenvironment no\n") == nil {
		t.Fatal("incomplete settings accepted")
	}
	if checkGlobalSSH(strings.Repeat("permituserenvironment no\n", 2)) == nil {
		t.Fatal("duplicates accepted")
	}
}

func TestPolicyForcesDispatcherForAnyAuthorizationSource(t *testing.T) {
	if !strings.Contains(sshPolicy, "ForceCommand /usr/local/bin/brine host serve") {
		t.Fatal("certificate or future authorization could bypass key-local forced command")
	}
}

func TestUnsafeEnvironmentRejectedAgainstRealOpenSSH(t *testing.T) {
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("sshd unavailable")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("keygen unavailable")
	}
	d := t.TempDir()
	key := filepath.Join(d, "hostkey")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	policy := filepath.Join(d, "policy.conf")
	if err := os.WriteFile(policy, []byte(sshPolicy), 0600); err != nil {
		t.Fatal(err)
	}
	for _, env := range []string{"AcceptEnv *", "AcceptEnv=LD_*", "SetEnv LD_PRELOAD=/fixture", "SetEnv=ENV=/fixture"} {
		path := filepath.Join(d, "main.conf")
		main := []byte("HostKey " + key + "\nPermitUserEnvironment no\nMatch Address 192.0.2.0/24\n" + env + "\n")
		if err := os.WriteFile(path, sshCandidate(main, policy), 0600); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(sshd, "-t", "-f", path).CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
		out, err := exec.Command(sshd, "-T", "-f", path, "-C", sshConnections[2]).Output()
		if err != nil {
			t.Fatal(err)
		}
		if err := checkForcedSSH(string(out)); err == nil {
			t.Fatalf("unsafe real sshd environment accepted: %s", env)
		}
	}
}

func TestIncludeNegationAndSourceManifestAgainstRealOpenSSH(t *testing.T) {
	sshd, err := exec.LookPath("sshd")
	if err != nil {
		t.Skip("OpenSSH server unavailable")
	}
	keygen, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("keygen unavailable")
	}
	d := t.TempDir()
	key := filepath.Join(d, "hostkey")
	if out, err := exec.Command(keygen, "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("keygen %v %s", err, out)
	}
	leaf := filepath.Join(d, "unsafe.conf")
	path := filepath.Join(d, "main")
	if err := os.WriteFile(leaf, []byte("Match Address 203.0.113.0/24\nAcceptEnv LD_PRELOAD\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, include := range []string{filepath.Join(d, "[!x]*.conf"), leaf} {
		if err := os.WriteFile(path, []byte("HostKey "+key+"\nInclude "+include+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(sshd, "-ddd", "-T", "-f", path, "-C", "user=brine,host=fixture,addr=203.0.113.1,laddr=198.51.100.1,lport=22")
		cmd.Env = append(os.Environ(), "LC_ALL=C")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("sshd %v %s", err, stderr.String())
		}
		if !strings.Contains(strings.ToLower(string(out)), "acceptenv ld_preload") {
			t.Fatal("sshd did not load unsafe negated-class fixture")
		}
		if err := checkSSHSourceTrace([]string{path, leaf}, stderr.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := (Prober{FS: inventory.HostFS{}}).sshAuditedSources(context.Background(), path); err == nil {
			t.Fatal("unsafe literal or unsupported negated-class accepted")
		}
	}
	if err := os.WriteFile(leaf, []byte("AcceptEnv LANG LC_*\n"), 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := (Prober{FS: inventory.HostFS{}}).sshAuditedSources(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(sshd, "-ddd", "-T", "-f", path)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if _, err := cmd.Output(); err != nil {
		t.Fatal(err)
	}
	if err := checkSSHSourceTrace(sources, stderr.String()); err != nil {
		t.Fatal(err)
	}
}
