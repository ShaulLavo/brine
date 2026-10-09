package enroll

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

type configFS struct {
	files map[string]string
	dirs  map[string][]fs.DirEntry
}

func (f configFS) ReadFile(_ context.Context, p string) ([]byte, error) {
	s, ok := f.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return []byte(s), nil
}
func (f configFS) ReadDir(_ context.Context, p string) ([]fs.DirEntry, error) {
	entries, ok := f.dirs[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return entries, nil
}
func (f configFS) Readlink(context.Context, string) (string, error) { return "", fs.ErrNotExist }
func TestSSHSourceChecks(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  bool
	}{
		{"# comment\nInclude /etc/ssh/sshd_config.d/*.conf\n", true},
		{"Include /etc/ssh/sshd_config.d/*.conf\nMatch Address 192.0.2.0/24\n AuthorizedKeysFile=.ssh/authorized_keys .local/keys\n AuthorizedKeysCommand=/fixture\n Include=relative.conf\n", true},
		{"PermitUserEnvironment no\n", false}, {"Include relative.conf\n", false},
	} {
		p := Prober{FS: configFS{files: map[string]string{"/etc/ssh/sshd_config": tt.input}, dirs: map[string][]fs.DirEntry{"/etc/ssh/sshd_config.d": nil}}}
		err := p.sshConfig(context.Background(), "/etc/ssh/sshd_config", nil, 0)
		if (err == nil) != tt.want {
			t.Fatalf("%q: %v", tt.input, err)
		}
	}
}
func TestSSHConfigurationFailures(t *testing.T) {
	p := Prober{FS: configFS{}}
	if err := p.sshConfig(context.Background(), "/missing", map[string]bool{}, 0); err == nil {
		t.Fatal("missing config accepted")
	}
	if err := p.sshConfig(context.Background(), "/missing", map[string]bool{}, 17); err == nil {
		t.Fatal("unbounded recursion")
	}
}
func TestAptTransaction(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  bool
	}{
		{"Inst podman (5.4.2 Debian:13/stable [arm64])\nInst passt (1.0 Debian:13/stable [arm64])\n", true},
		{"Inst existing [1.0] (2.0 Debian:13/stable [arm64])\n", false},
		{"Remv existing [1.0]\n", false},
		{"Inst --bad (1.0 source)\n", false},
		{"Inst podman\n", false},
		{"Inst podman (;touch source)\n", false},
	} {
		_, err := aptInstalls(tt.input)
		if (err == nil) != tt.want {
			t.Errorf("%q %v", tt.input, err)
		}
	}
}
func TestPublicKeyRefusesOptionsAndShellInput(t *testing.T) {
	for _, key := range []string{"command=\"shell\" ssh-ed25519 AAAA", "ssh-ed25519 AAAA\n", "ssh-rsa AAAA", "ssh-ed25519 AAAA x y"} {
		if _, err := PublicKey(key); err == nil {
			t.Errorf("accepted %q", key)
		}
	}
}

func TestAlternateAuthorizationSourcesRefused(t *testing.T) {
	for _, input := range []string{
		"AuthorizedKeysFile .ssh/authorized_keys .config/authorized_keys\n",
		"Match User brine\n AuthorizedKeysFile .ssh/authorized_keys .local/keys\n",
		"Match Address 192.0.2.0/24\n AuthorizedKeysCommand /usr/local/bin/key-provider\n",
		"AuthorizedKeysFile .ssh/authorized_keys2\n",
	} {
		p := Prober{FS: configFS{files: map[string]string{"/etc/ssh/sshd_config": input}}}
		if err := p.sshConfig(context.Background(), "/etc/ssh/sshd_config", map[string]bool{}, 0); err == nil {
			t.Fatalf("alternate authorization bypass accepted: %q", input)
		}
	}
}

func TestSSHRequiresUnconditionalFirstDebianInclude(t *testing.T) {
	for _, input := range []string{
		"AuthorizedKeysFile=.ssh/authorized_keys\nInclude /etc/ssh/sshd_config.d/*.conf\n",
		"Match Address 192.0.2.0/24\nInclude /etc/ssh/sshd_config.d/*.conf\n",
		"Include=/etc/ssh/sshd_config.d/*.conf\n",
	} {
		p := Prober{FS: configFS{files: map[string]string{"/etc/ssh/sshd_config": input}}}
		if err := p.sshConfig(context.Background(), "/etc/ssh/sshd_config", map[string]bool{}, 0); err == nil {
			t.Fatalf("unsafe first directive accepted: %q", input)
		}
	}
}

func TestEarlierDropinRefused(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "00-aaa.conf"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	p := Prober{FS: configFS{files: map[string]string{"/etc/ssh/sshd_config": "Include /etc/ssh/sshd_config.d/*.conf\n"}, dirs: map[string][]fs.DirEntry{"/etc/ssh/sshd_config.d": entries}}}
	if p.sshConfig(context.Background(), "/etc/ssh/sshd_config", nil, 0) == nil {
		t.Fatal("earlier policy can override Brine")
	}
}
