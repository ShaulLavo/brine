package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentResumeIsPrivateLockedAndDoesNotRetainCredentials(t *testing.T) {
	flags := operationFlags{target: "fixture", configDir: filepath.Join(t.TempDir(), "targets")}
	identity := []byte("private-spec-identity")
	resume, err := flags.openDeploymentResume(identity)
	if err != nil {
		t.Fatal(err)
	}
	resume.journal = deploymentJournal{ReviewHash: "sha256:" + strings.Repeat("a", 64), Steps: []deploymentStep{{Name: "preparation", PlanID: "sha256:" + strings.Repeat("b", 64), Key: "prep-key"}, {Name: "release"}}}
	if err = resume.save(); err != nil {
		t.Fatal(err)
	}
	if second, err := flags.openDeploymentResume(identity); err == nil {
		second.close()
		t.Fatal("concurrent deployment acquired journal")
	}
	resume.close()
	next, err := flags.openDeploymentResume(identity)
	if err != nil {
		t.Fatal(err)
	}
	if next.journal.Steps[0].Key != "prep-key" {
		t.Fatal("lost intent before acceptance")
	}
	next.close()
	entries, err := filepath.Glob(filepath.Join(flags.configDir, "deploys", "*", "journal.json"))
	if err != nil || len(entries) != 1 {
		t.Fatal(entries, err)
	}
	info, err := os.Stat(entries[0])
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("nonprivate journal", err)
	}
	raw, err := os.ReadFile(entries[0])
	if err != nil || strings.Contains(string(raw), "private-spec") {
		t.Fatal("journal retained source input", err)
	}
	if err = os.Remove(entries[0]); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("lock", entries[0]); err != nil {
		t.Fatal(err)
	}
	if next, err := flags.openDeploymentResume(identity); err == nil {
		next.close()
		t.Fatal("accepted journal symlink")
	}
}
