package enroll

import (
	"context"
	"errors"
	"github.com/ShaulLavo/brine/internal/localexec"
	"regexp"
	"strings"
)

var packageName = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]*(?::(?:amd64|arm64|all))?$`)
var packageVersion = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+:~_-]*$`)

func aptInstalls(output string) ([]Package, error) {
	packages := []Package{}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "Remv" {
			return nil, errors.New("apt transaction removes existing packages")
		}
		if fields[0] != "Inst" {
			continue
		}
		if len(fields) < 3 || !packageName.MatchString(fields[1]) || !strings.HasPrefix(fields[2], "(") {
			return nil, errors.New("apt transaction upgrades an existing package or cannot be parsed")
		}
		version := strings.TrimPrefix(fields[2], "(")
		if !packageVersion.MatchString(version) {
			return nil, errors.New("invalid apt version")
		}
		packages = append(packages, Package{Name: fields[1], Version: version})
	}
	return packages, nil
}
func installed(ctx context.Context, runner localexec.StdoutRunner, name string) (string, error) {
	out, err := runner.RunStdout(ctx, "dpkg-query", "-W", "-f=${db:Status-Status}\t${Version}", name)
	if err != nil {
		var e *localexec.Error
		if errors.As(err, &e) && e.ExitCode == 1 {
			return "", nil
		}
		return "", errors.New("package state is unknown")
	}
	fields := strings.Fields(out)
	if len(fields) != 2 {
		return "", errors.New("invalid dpkg state")
	}
	if fields[0] != "installed" {
		return "", nil
	}
	return fields[1], nil
}
