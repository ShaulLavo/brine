package cli

import "testing"

func TestHostAdmissionSettleRegisteredWithoutRunnerOpen(t *testing.T) {
	args := []string{"host", "admission-settle"}
	command, _, err := NewRootCommand(Dependencies{}).Find(args)
	if err != nil || command.CommandPath() != "brine host admission-settle" || !command.Hidden {
		t.Fatal("root settle command missing", command, err)
	}
	if command.Args == nil || command.Args(command, []string{"unexpected"}) == nil {
		t.Fatal("settle accepted arguments")
	}
	if HostServeRequested(args) || HostRuntimeRequested(args) || HostStartupRequested(args) {
		t.Fatal("root settle selected a runner opener")
	}
}
