package quadlet

import (
	"errors"
	"reflect"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
)

func bindManagedFrontend(d policy.Desired, p plan.Plan) (persistentRender, error) {
	m := p.Managed
	if policy.ValidateManagedDesired(d) != nil || m == nil || !m.Valid() || m.State != data.EngineInitialized || m.Frontend == nil || m.Database.Name != d.SpacetimeDB.Name || m.Database.Root != d.SpacetimeDB.PersistentRoot || m.Frontend.Port != p.HostPort || !reflect.DeepEqual(m.Frontend.Image, p.Image) || !reflect.DeepEqual(m.Frontend.Secrets, p.Secrets) || p.Runtime != nil || len(p.DataMounts) != 0 || len(p.DataSchemas) != 0 {
		return persistentRender{}, errors.New("quadlet: managed frontend ownership required")
	}
	return persistentRender{managedBinding: m.Database.BindingID, services: []string{"brine-engine-" + string(m.Database.BindingID) + ".service"}}, nil
}
