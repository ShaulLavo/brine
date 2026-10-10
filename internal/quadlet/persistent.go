package quadlet

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/data"
	"github.com/ShaulLavo/brine/internal/plan"
	"github.com/ShaulLavo/brine/internal/policy"
	"github.com/ShaulLavo/brine/internal/replication"
)

type persistentRender struct {
	runtime     *data.RuntimeIdentity
	incarnation data.AppIncarnationID
	mounts      []data.Mount
	services    []string
}

func bindPersistent(d policy.Desired, p plan.Plan) (persistentRender, error) {
	invalid := fmt.Errorf("quadlet: persistent runtime or mounts differ from admitted input")
	if d.Stateless() {
		if p.Runtime != nil || len(p.DataMounts) != 0 {
			return persistentRender{}, invalid
		}
		return persistentRender{}, nil
	}
	if d.Runtime == nil || p.Runtime == nil || d.Runtime.Validate() != nil || *d.Runtime != *p.Runtime || len(d.Databases) == 0 || len(p.DataMounts) != len(d.Databases) {
		return persistentRender{}, invalid
	}
	declarations := map[data.DatabaseName]data.Database{}
	for _, database := range d.Databases {
		if database.Validate() != nil {
			return persistentRender{}, invalid
		}
		if _, duplicate := declarations[database.Name]; duplicate {
			return persistentRender{}, invalid
		}
		declarations[database.Name] = database
	}
	mounts := slices.Clone(p.DataMounts)
	slices.SortFunc(mounts, func(a, b data.Mount) int { return strings.Compare(string(a.Database.Name), string(b.Database.Name)) })
	r := persistentRender{runtime: p.Runtime, mounts: mounts}
	bindings := map[data.ReplicaBindingID]bool{}
	databases := map[data.DatabaseID]bool{}
	for _, m := range mounts {
		b := m.Database
		database, exists := declarations[b.Name]
		relative, err := data.RelativeDirectory(b.IncarnationID, b.DatabaseID)
		service, serviceErr := replication.ServiceName(string(m.BindingID))
		if !exists || err != nil || serviceErr != nil || b.Root != database.PersistentRoot || b.MountPath != database.MountPath || b.Filename != database.Filename || b.RelativeDirectory != relative || m.BindingID != b.ReplicaBindingID || m.HostPath != path.Join(string(b.Root), relative) || m.ContainerPath != database.MountPath || strings.ContainsAny(m.HostPath+string(m.ContainerPath), ":\x00\r\n") || bindings[m.BindingID] || databases[b.DatabaseID] {
			return persistentRender{}, invalid
		}
		if r.incarnation != "" && r.incarnation != b.IncarnationID {
			return persistentRender{}, invalid
		}
		for _, prior := range r.mounts {
			if prior.Database.Name == b.Name {
				continue
			}
			left, right := string(prior.ContainerPath), string(m.ContainerPath)
			if left == right || strings.HasPrefix(left, right+"/") || strings.HasPrefix(right, left+"/") {
				return persistentRender{}, invalid
			}
		}
		r.incarnation = b.IncarnationID
		delete(declarations, b.Name)
		bindings[m.BindingID] = true
		databases[b.DatabaseID] = true
		r.services = append(r.services, service)
	}
	return r, nil
}
