package quadlet

import (
	"errors"
	"fmt"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
)

// RenderManagedServicePreview renders a Podman supervisor service, not Quadlet.
func RenderManagedServicePreview(preview plan.ManagedRenderPreview, binding data.ReplicaBindingID) (Unit, error) {
	d, _, _, err := preview.Values()
	if err != nil || !data.ValidID(string(binding)) {
		return Unit{}, errors.New("managed service preview: binding required")
	}
	r := d.SpacetimeDB.Resources
	name := "brine-engine-" + string(binding) + ".service"
	content := fmt.Sprintf("# Brine managed engine preview; execution not admitted\n[Unit]\nDescription=Brine managed engine %s\n\n[Service]\nType=simple\nExecStart=/usr/local/bin/brine host engine-exec %s\nRestart=on-failure\nKillSignal=SIGINT\nKillMode=mixed\nTimeoutStopSec=%d\nMemoryMax=%dM\nTasksMax=%d\nUMask=0077\n\n[Install]\nWantedBy=default.target\n", d.Name, binding, r.StopTimeoutSeconds, r.MemoryMB, r.PIDsLimit)
	return Unit{name: name, content: content}, nil
}

func RenderManagedFrontendPreview(preview plan.ManagedRenderPreview) (Unit, error) {
	d, _, appPort, err := preview.Values()
	if err != nil || appPort == 0 {
		return Unit{}, errors.New("managed frontend preview: frontend required")
	}
	if len(d.Secrets) != 0 {
		return Unit{}, errors.New("managed frontend preview: immutable secret versions are not bound")
	}
	content := fmt.Sprintf("# Brine managed frontend preview; execution not admitted\n[Unit]\nDescription=Brine app %s\n\n[Container]\nImage=%s\nPublishPort=127.0.0.1:%d:%d\nPidsLimit=%d\nPodmanArgs=--memory=%dm\n", d.Name, d.Image, appPort, d.ContainerPort, d.Resources.PIDsLimit, d.Resources.MemoryMB)
	for _, value := range d.DatabaseEnvironmentValues() {
		content += "Environment=" + quoteAssignment(value.Name+"="+value.Value) + "\n"
	}
	for _, value := range d.Environment {
		content += "Environment=" + quoteAssignment(value.Name+"="+value.Value) + "\n"
	}
	content += "\n[Service]\nRestart=on-failure\n\n[Install]\nWantedBy=default.target\n"
	return Unit{name: string(d.Name) + ".container", content: content}, nil
}
