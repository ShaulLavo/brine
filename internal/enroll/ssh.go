package enroll

import (
	"errors"
	"fmt"
	"strings"
)

const sshDir = "/etc/ssh/brine"
const sshKeyDir = sshDir + "/authorized_keys"
const sshKeyPath = sshKeyDir + "/brine"
const sshIdentityPath = sshDir + "/inventory-key"
const sshPolicyPath = "/etc/ssh/sshd_config.d/00-brine-brine.conf"
const sshPolicyCandidate = "/var/lib/brine-enrollment/candidate-ssh-policy.conf"
const sshMainCandidate = "/var/lib/brine-enrollment/candidate-sshd.conf"
const sshPolicy = `Match User brine
    ForceCommand /usr/local/bin/brine host serve
    AuthorizedKeysFile /etc/ssh/brine/authorized_keys/%u
    AuthorizedKeysCommand none
    AuthorizedPrincipalsFile none
    AllowTcpForwarding no
    AllowAgentForwarding no
    X11Forwarding no
    PermitTTY no
    PermitTunnel no
    GatewayPorts no
    AllowStreamLocalForwarding no
Match all
`

var sshConnections = []string{
	"user=brine,host=localhost,addr=127.0.0.1,laddr=127.0.0.1,lport=22",
	"user=brine,host=localhost,addr=::1,laddr=::1,lport=22",
	"user=brine,host=remote.example,addr=192.0.2.1,laddr=198.51.100.1,lport=22",
	"user=brine,host=remote.example,addr=2001:db8::1,laddr=2001:db8::2,lport=2222",
}

func checkSSHValues(out string, want map[string]string) error {
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		f[0] = strings.ToLower(f[0])
		if v, ok := want[f[0]]; ok {
			if len(f) < 2 || strings.Join(f[1:], " ") != v || seen[f[0]] {
				return errors.New("SSH safe setting overridden")
			}
			seen[f[0]] = true
		}
	}
	if len(seen) != len(want) {
		for k := range want {
			if !seen[k] {
				return fmt.Errorf("SSH safe setting unavailable: %s", k)
			}
		}
		return errors.New("SSH safe settings incomplete")
	}
	return nil
}
func checkGlobalSSH(out string) error {
	if err := checkSSHValues(out, map[string]string{"permituserenvironment": "no"}); err != nil {
		return err
	}
	return checkSSHEnvironment(out)
}
func checkForcedSSH(out string) error {
	if err := checkGlobalSSH(out); err != nil {
		return err
	}
	return checkSSHValues(out, map[string]string{"forcecommand": "/usr/local/bin/brine host serve", "authorizedkeysfile": "/etc/ssh/brine/authorized_keys/%u", "authorizedkeyscommand": "none", "authorizedprincipalsfile": "none", "allowtcpforwarding": "no", "allowagentforwarding": "no", "x11forwarding": "no", "permittty": "no", "permittunnel": "no", "gatewayports": "no", "allowstreamlocalforwarding": "no"})
}

// Client environment permits locale patterns and exact display-only names.
// Server SetEnv assignments remain limited to concrete locale variables.
// A subset (including an empty AcceptEnv list) is safe; additive extras are not.
func checkSSHEnvironment(out string) error {
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		keyword := strings.ToLower(f[0])
		if keyword == "acceptenv" {
			for _, pattern := range f[1:] {
				if pattern != "LANG" && pattern != "LC_*" && pattern != "COLORTERM" && pattern != "NO_COLOR" {
					return errors.New("SSH AcceptEnv must allow only LANG, LC_*, COLORTERM and NO_COLOR; unsafe client environment refused")
				}
			}
		}
		if keyword == "setenv" {
			if len(f) == 2 && f[1] == "none" {
				continue
			}
			for _, assignment := range f[1:] {
				name, _, ok := strings.Cut(assignment, "=")
				if !ok || !safeLocaleVariable(name) {
					return errors.New("SSH SetEnv must set only locale variables; loader and shell environment refused")
				}
			}
		}
	}
	return nil
}
func safeLocaleVariable(name string) bool {
	if name == "LANG" {
		return true
	}
	if !strings.HasPrefix(name, "LC_") || len(name) == 3 {
		return false
	}
	for _, c := range name[3:] {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}
