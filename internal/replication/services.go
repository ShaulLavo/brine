package replication

import (
	"context"
	"strings"

	"github.com/ShaulLavo/brine/internal/localexec"
	"github.com/ShaulLavo/brine/internal/systemd"
)

type Services struct {
	session localexec.Session
	client  *systemd.Client
}

func NewServices(session localexec.Session) *Services {
	return &Services{session: session, client: systemd.New(session)}
}
func replicaService(name string) (systemd.Unit, error) {
	id := strings.TrimSuffix(strings.TrimPrefix(name, "brine-litestream-"), ".service")
	expected, err := ServiceName(id)
	if err != nil || expected != name {
		return systemd.Unit{}, ErrInvalid
	}
	return systemd.ParseUnit(name)
}
func (s *Services) Start(ctx context.Context, name string) error {
	unit, err := replicaService(name)
	if err != nil {
		return err
	}
	return s.client.Start(ctx, unit)
}
func (s *Services) Stop(ctx context.Context, name string) error {
	unit, err := replicaService(name)
	if err != nil {
		return err
	}
	return s.client.Stop(ctx, unit)
}
func (s *Services) ReplicaStopped(ctx context.Context, name string) (bool, error) {
	if _, err := replicaService(name); err != nil {
		return false, err
	}
	r, err := s.session.Execute(ctx, "/usr/bin/systemctl", []string{"--user", "show", name, "--property=LoadState,ActiveState,SubState,MainPID,ControlPID,ControlGroup,Job"}, nil, false)
	if err != nil || r.ExitCode != 0 || r.Truncated {
		return false, ErrPermit
	}
	fields := map[string]string{}
	for _, line := range strings.Split(strings.TrimSpace(r.Stdout), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return false, ErrPermit
		}
		switch key {
		case "LoadState", "ActiveState", "SubState", "MainPID", "ControlPID", "ControlGroup", "Job":
		default:
			return false, ErrPermit
		}
		if _, seen := fields[key]; seen {
			return false, ErrPermit
		}
		fields[key] = value
	}
	if len(fields) != 7 || fields["LoadState"] != "loaded" || fields["MainPID"] != "0" || fields["ControlPID"] != "0" || fields["ControlGroup"] != "" || fields["Job"] != "" {
		return false, ErrPermit
	}
	if fields["ActiveState"] == "inactive" && fields["SubState"] == "dead" {
		return true, nil
	}
	if fields["ActiveState"] == "failed" && fields["SubState"] == "failed" {
		return true, nil
	}
	return false, ErrPermit
}
