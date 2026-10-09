package enroll

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
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
		{"Include /etc/ssh/sshd_config.d/*.conf\nMatch Address 192.0.2.0/24\n AuthorizedKeysFile=.ssh/authorized_keys .local/keys\n AuthorizedKeysCommand=/fixture\n Include=relative.conf\n", false},
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

func TestEnvironmentAuditCoversUnsampledMatchAndEqualsSyntax(t *testing.T) {
	for _, line := range []string{"AcceptEnv *", "AcceptEnv=LD_*", "AcceptEnv = \"LD_PRELOAD\"", "SetEnv=ENV=/fixture", "SetEnv PATH=/fixture"} {
		p := Prober{FS: configFS{files: map[string]string{"/etc/ssh/sshd_config": "Include /etc/ssh/sshd_config.d/*.conf\nMatch Address 203.0.113.0/24\nInclude=\"/etc/ssh/remote.conf\"\n", "/etc/ssh/remote.conf": line + "\n"}, dirs: map[string][]fs.DirEntry{"/etc/ssh/sshd_config.d": nil}}}
		if p.sshConfig(context.Background(), "/etc/ssh/sshd_config", nil, 0) == nil {
			t.Fatalf("unsafe unsampled branch accepted: %s", line)
		}
	}
}

func TestEnvironmentSourcesQuotedGlobAndHashPath(t *testing.T) {
	for _, path := range []string{"/etc/ssh/unsafe#branch.conf", "relative.conf"} {
		resolved := path
		if !filepath.IsAbs(path) {
			resolved = "/etc/ssh/" + path
		}
		p := Prober{FS: configFS{files: map[string]string{"/etc/ssh/sshd_config": "Include /etc/ssh/sshd_config.d/*.conf\nInclude " + path + "\n", resolved: "Match Address 203.0.113.99\nAcceptEnv=LD_*\n"}, dirs: map[string][]fs.DirEntry{"/etc/ssh/sshd_config.d": nil}}}
		if p.sshConfig(context.Background(), "/etc/ssh/sshd_config", nil, 0) == nil {
			t.Fatal("unsafe included path accepted")
		}
	}
	for _, line := range []string{"AcceptEnv LANG LC_* # safe", "SetEnv LANG=C LC_TIME=C", "AcceptEnv=\"LANG\" \"LC_*\""} {
		f, err := sshSourceFields(line)
		if err != nil {
			t.Fatal(err)
		}
		if err := checkSSHEnvironment(strings.Join(f, " ")); err != nil {
			t.Fatal(err)
		}
	}
	for _, line := range []string{"Include \"unterminated", "Include /unsafe\\path"} {
		if _, err := sshSourceFields(line); err == nil {
			t.Fatal("ambiguous include accepted")
		}
	}
}

func TestIncludePOSIXNegationMustFailClosed(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "unsafe.conf"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	p := Prober{FS: configFS{files: map[string]string{"/etc/ssh/sshd_config": "Include /etc/ssh/sshd_config.d/*.conf\nInclude /fixture/[!x]*.conf\n", "/fixture/unsafe.conf": "Match Address 203.0.113.0/24\nAcceptEnv LD_PRELOAD\n"}, dirs: map[string][]fs.DirEntry{"/etc/ssh/sshd_config.d": nil, "/fixture": entries}}}
	if p.sshConfig(context.Background(), "/etc/ssh/sshd_config", nil, 0) == nil {
		t.Fatal("Go/POSIX negated-class mismatch skips unsafe branch")
	}
}
func TestIncludesRestrictedToLiteralOrDebianGlob(t *testing.T) {
	for _, pattern := range []string{"/fixture/[!x]*.conf", "/fixture/[[:alpha:]]*.conf", "/fixture/?.conf", "/fixture/{a,b}.conf", "~/config", "relative.conf", "/fixture/*.conf", "/etc/ssh/sshd_config.d/**.conf"} {
		p := Prober{FS: configFS{files: map[string]string{"/root.conf": "Include " + pattern + "\n"}, dirs: map[string][]fs.DirEntry{"/fixture": nil, "/etc/ssh/sshd_config.d": nil}}}
		if p.sshEnvironmentSources(context.Background(), "/root.conf") == nil {
			t.Fatalf("unsupported pattern accepted: %s", pattern)
		}
	}
	p := Prober{FS: configFS{files: map[string]string{"/root.conf": "Include /leaf.conf\n", "/leaf.conf": "Include /second.conf\n", "/second.conf": "AcceptEnv LANG\n"}}}
	if p.sshEnvironmentSources(context.Background(), "/root.conf") == nil {
		t.Fatal("nested include accepted")
	}
}

func TestDebianIncludeMatchesGlob3OrderAndDotRule(t *testing.T) {
	d := t.TempDir()
	for _, name := range []string{"z.conf", "a.conf", ".hidden.conf", "ignored.txt"} {
		if err := os.WriteFile(filepath.Join(d, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(d)
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately unsorted filesystem results must not change glob(3)'s C-locale order.
	for i, j := 0, len(entries)-1; i < j; i, j = i+1, j-1 {
		entries[i], entries[j] = entries[j], entries[i]
	}
	p := Prober{FS: configFS{files: map[string]string{"/root.conf": "Include /etc/ssh/sshd_config.d/*.conf\nInclude /literal.conf\n", "/etc/ssh/sshd_config.d/a.conf": "AcceptEnv LANG\n", "/etc/ssh/sshd_config.d/z.conf": "AcceptEnv LC_*\n", "/etc/ssh/sshd_config.d/.hidden.conf": "AcceptEnv LD_PRELOAD\n", "/literal.conf": "AcceptEnv NO_COLOR\n"}, dirs: map[string][]fs.DirEntry{"/etc/ssh/sshd_config.d": entries}}}
	got, err := p.sshAuditedSources(context.Background(), "/root.conf")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/root.conf", "/etc/ssh/sshd_config.d/a.conf", "/etc/ssh/sshd_config.d/z.conf", "/literal.conf"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("manifest %v", got)
	}
	p.FS = configFS{files: map[string]string{"/root.conf": "Include /etc/ssh/sshd_config.d/.hidden.conf\n", "/etc/ssh/sshd_config.d/.hidden.conf": "AcceptEnv LD_PRELOAD\n"}}
	if p.sshEnvironmentSources(context.Background(), "/root.conf") == nil {
		t.Fatal("literal dotfile not audited")
	}
}
func TestSSHSourceTraceRequiresCompleteExactManifest(t *testing.T) {
	sources := []string{"/main.conf", "/leaf.conf"}
	trace := "debug2: load_server_config: filename /main.conf\r\ndebug2: load_server_config: filename /leaf.conf\r\n"
	if err := checkSSHSourceTrace(sources, trace+trace); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "debug2: load_server_config: filename /main.conf\n", trace + "debug2: load_server_config: filename /unsafe.conf\n", "debug2: load_server_config: filename /leaf.conf\ndebug2: load_server_config: filename /main.conf\n"} {
		if checkSSHSourceTrace(sources, bad) == nil {
			t.Fatal("missing, extra, or reordered sources accepted")
		}
	}
}
