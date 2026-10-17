package cdntest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/ShaulLavo/brine/internal/cdn"
)

type Driver struct {
	fixture *Fixture
	profile cdn.ProfileScope
	traffic bool
	control string
}

var _ cdn.EdgeDriver = (*Driver)(nil)
var _ cdn.TrafficDriver = (*Driver)(nil)

func (d *Driver) Profile() cdn.ProfileScope { return d.profile }

func (d *Driver) call(ctx context.Context, method, path string, value any, version string, result any) error {
	var body []byte
	if value != nil {
		body, _ = json.Marshal(value)
	}
	request, err := http.NewRequestWithContext(ctx, method, d.control+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("If-Match", version)
	response, err := d.fixture.Client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return cdn.ErrConflict
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, bodyLimit))
	decoder.DisallowUnknownFields()
	if decoder.Decode(result) != nil || decoder.Decode(new(any)) != io.EOF {
		return cdn.ErrInvalid
	}
	return nil
}

func (d *Driver) Observe(ctx context.Context, attachment cdn.Attachment) (cdn.Observation, error) {
	var result cdn.Observation
	err := d.call(ctx, http.MethodGet, "/state/"+string(d.profile.Reference), nil, "", &result)
	return result, err
}

func (d *Driver) identity(attachment cdn.Attachment) cdn.OwnedResource {
	kind, name, id := cdn.EdgeConfiguration, string(d.profile.Reference), string(d.profile.Reference)
	if d.traffic {
		kind, name, id = cdn.HostnameTraffic, attachment.Binding.Hostname, attachment.Binding.TrafficAuthority.ResourceID
	}
	return cdn.OwnedResource{Kind: kind, ID: id, ExactName: name, Owner: attachment.Owner, Version: "0"}
}

func (d *Driver) resource(attachment cdn.Attachment, observation cdn.Observation) cdn.OwnedResource {
	if len(observation.Resources) != 0 {
		return observation.Resources[0]
	}
	return d.identity(attachment)
}

func (d *Driver) Actions(attachment cdn.Attachment, observed cdn.Observation) ([]cdn.PlannedStep, error) {
	if attachment.Binding.Validate() != nil || observed.Profile != d.profile {
		return nil, cdn.ErrInvalid
	}
	if !d.traffic && attachment.Mode == cdn.Direct {
		return nil, nil
	}
	resource := d.resource(attachment, observed)
	if resource.Owner != attachment.Owner {
		return nil, cdn.ErrConflict
	}
	kind := cdn.PutEdge
	if d.traffic {
		kind = cdn.SetTraffic
	}
	desired := configurationHash(attachment, d.profile)
	if desired == observed.Fingerprint {
		return nil, nil
	}
	return []cdn.PlannedStep{{Kind: kind, Profile: d.profile, ResourceKind: resource.Kind, ExactName: resource.ExactName, Desired: desired, Precondition: cdn.InitialObservation{Before: observed.Fingerprint, Resource: resource}}}, nil
}

func configurationHash(attachment cdn.Attachment, profile cdn.ProfileScope) cdn.Fingerprint {
	wire, _ := json.Marshal(struct {
		Attachment cdn.Attachment
		Profile    cdn.ProfileScope
	}{attachment, profile})
	return cdn.Hash(wire)
}

func (d *Driver) Seal(ctx context.Context, attachment cdn.Attachment, step cdn.PlannedStep, prior cdn.OwnedReceipt) (cdn.SealedAction, error) {
	current, err := d.Observe(ctx, attachment)
	if err != nil {
		return cdn.SealedAction{}, err
	}
	resource := d.resource(attachment, current)
	if resource.Owner != attachment.Owner || step.Profile != d.profile {
		return cdn.SealedAction{}, cdn.ErrConflict
	}
	sealed := cdn.SealedAction{Current: current, Action: cdn.Action{Kind: step.Kind, Attachment: attachment, Profile: d.profile, Resource: resource, Expected: current.Fingerprint, Desired: step.Desired}}
	if prior.Effect != "" {
		prerequisite, err := d.fixture.driverFor(prior.Profile).Observe(ctx, attachment)
		if err != nil {
			return cdn.SealedAction{}, err
		}
		sealed.Prerequisites = []cdn.Observation{prerequisite}
	}
	if err := cdn.ValidateSealed(attachment, step, sealed, prior); err != nil {
		return cdn.SealedAction{}, err
	}
	return sealed, nil
}

func (f *Fixture) driverFor(profile cdn.ProfileScope) *Driver {
	if profile == f.EdgeA.profile {
		return f.EdgeA
	}
	if profile == f.EdgeB.profile {
		return f.EdgeB
	}
	return f.Traffic
}

func (d *Driver) Execute(ctx context.Context, permit cdn.Permit) (cdn.EffectResult, error) {
	if permit.Action.Profile != d.profile || permit.Claim == "" || permit.Effect == "" {
		return nil, cdn.ErrInvalid
	}
	kind := cdn.PutEdge
	if d.traffic {
		kind = cdn.SetTraffic
	}
	identity := d.identity(permit.Action.Attachment)
	if permit.Action.Kind != kind || permit.Action.Resource.Kind != identity.Kind || permit.Action.Resource.ID != identity.ID || permit.Action.Resource.ExactName != identity.ExactName {
		return nil, cdn.ErrInvalid
	}
	var operation nativeOperation
	err := d.call(ctx, http.MethodPost, "/state/"+string(d.profile.Reference), permit, permit.Action.Resource.Version, &operation)
	if err != nil {
		return cdn.Uncertain{Effect: permit.Effect, Code: "http_reply_unknown"}, nil
	}
	if operation.Refused {
		return cdn.Refused{Effect: permit.Effect, Code: "conditional_conflict"}, nil
	}
	return cdn.Acknowledged{Effect: permit.Effect, Receipt: cdn.Hash([]byte("native:" + string(permit.Effect))), Resources: []cdn.OwnedResource{operation.Resource}}, nil
}

func (d *Driver) Inspect(ctx context.Context, permit cdn.Permit) (cdn.Inspection, error) {
	var operation nativeOperation
	if err := d.call(ctx, http.MethodGet, "/effect/"+string(permit.Effect), nil, "", &operation); err != nil {
		return nil, err
	}
	if operation.Permit.Effect != "" && !reflect.DeepEqual(operation.Permit, permit) {
		return nil, cdn.ErrConflict
	}
	current, err := d.Observe(ctx, permit.Action.Attachment)
	if err != nil {
		return nil, err
	}
	if operation.Complete && !operation.Refused && current.Fingerprint == permit.Action.Desired {
		return cdn.Completed{Effect: permit.Effect, Current: current, Evidence: cdn.Hash([]byte("completed:" + string(permit.Effect) + ":" + operation.Resource.Version))}, nil
	}
	relation := cdn.Diverged
	if current.Fingerprint == permit.Action.Expected {
		relation = cdn.Before
	}
	if current.Fingerprint == permit.Action.Desired {
		relation = cdn.Desired
	}
	return cdn.Observed{Effect: permit.Effect, Current: current, Relation: relation}, nil
}

func (d *Driver) Probe(ctx context.Context, qualification cdn.Qualification) (cdn.PublicProof, error) {
	if qualification.Validate() != nil || d.traffic {
		return cdn.PublicProof{}, cdn.ErrInvalid
	}
	origin, err := d.get(ctx, d.fixture.OriginURL, qualification)
	if err != nil {
		return cdn.PublicProof{}, err
	}
	first, err := d.get(ctx, d.fixture.GatewayURL, qualification)
	if err != nil {
		return cdn.PublicProof{}, err
	}
	second, err := d.get(ctx, d.fixture.GatewayURL, qualification)
	if err != nil {
		return cdn.PublicProof{}, err
	}
	hashes := cdn.Hash(origin.Body) == qualification.Sample.Digest && cdn.Hash(first.Body) == qualification.Sample.Digest && cdn.Hash(second.Body) == qualification.Sample.Digest
	return cdn.PublicProof{Method: cdn.OperatorHTTP, Sample: qualification.Sample, Source: d.profile, ObservedAt: time.Now().UTC(), Revision: qualification.Attachment.Revision, OriginHealthy: origin.Status == 200, EdgeHealthy: first.Status == 200 && second.Status == 200 && second.Header.Get("X-CDN-Endpoint") == string(d.profile.Reference), FullGETs: 2, HashesVerified: hashes, LaterHit: second.Header.Get("X-CDN-Cache") == "HIT", Evidence: cdn.Hash(second.Body)}, nil
}

func (d *Driver) get(ctx context.Context, endpoint string, q cdn.Qualification) (Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+q.Attachment.Binding.Hostname+q.Sample.Path, nil)
	if err != nil {
		return Response{}, err
	}
	request.Header.Set("Cache-Control", "no-cache, no-store")
	request.Header.Set("Pragma", "no-cache")
	return d.fixture.fetch(request, endpoint)
}

func (f *Fixture) serveControl(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/effect/") {
		f.serveEffect(w, r)
		return
	}
	profile := cdn.ProfileRef(strings.TrimPrefix(r.URL.Path, "/state/"))
	f.mu.Lock()
	n := f.nodes[profile]
	f.mu.Unlock()
	if n == nil {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodGet {
		f.mu.Lock()
		result := snapshot(n)
		f.mu.Unlock()
		encode(w, result)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var permit cdn.Permit
	decoder := json.NewDecoder(io.LimitReader(r.Body, bodyLimit))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&permit) != nil || decoder.Decode(new(any)) != io.EOF || permit.Action.Profile != n.profile {
		w.WriteHeader(400)
		return
	}
	f.accept(w, r, n, permit)
}

func snapshot(n *node) cdn.Observation {
	var resources []cdn.OwnedResource
	if n.resource.ID != "" {
		resources = []cdn.OwnedResource{n.resource}
	}
	return cdn.Observation{Profile: n.profile, Resources: resources, Fingerprint: n.fingerprint, ObservedAt: time.Now().UTC()}
}

func (f *Fixture) serveEffect(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	operation := f.operations[cdn.EffectID(strings.TrimPrefix(r.URL.Path, "/effect/"))]
	var result nativeOperation
	if operation != nil {
		result = *operation
	}
	f.mu.Unlock()
	encode(w, result)
}

func (f *Fixture) accept(w http.ResponseWriter, r *http.Request, n *node, permit cdn.Permit) {
	f.mu.Lock()
	previous := f.operations[permit.Effect]
	if previous != nil {
		f.replay(w, previous, permit)
		return
	}
	resource := permit.Action.Resource
	resource.Version = strconv.FormatUint(n.version+1, 10)
	resource.Fingerprint = permit.Action.Desired
	operation := &nativeOperation{Permit: permit, Resource: resource}
	operation.Refused = r.Header.Get("If-Match") != strconv.FormatUint(n.version, 10) || permit.Action.Expected != n.fingerprint || resource.Owner != permit.Action.Attachment.Owner
	if permit.Action.Desired != configurationHash(permit.Action.Attachment, n.profile) {
		operation.Refused = true
	}
	if n.resource.ID != "" && n.resource.Owner != resource.Owner {
		operation.Refused = true
	}
	f.operations[permit.Effect] = operation
	gate, drop := n.gate, n.drop
	n.gate, n.drop = nil, false
	result := *operation
	if !operation.Refused {
		f.wg.Add(1)
		go f.complete(n, operation, gate)
	}
	f.mu.Unlock()
	if drop {
		connection, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = connection.Close()
		}
		return
	}
	encode(w, result)
}

func (f *Fixture) replay(w http.ResponseWriter, previous *nativeOperation, permit cdn.Permit) {
	result, same := *previous, reflect.DeepEqual(previous.Permit, permit)
	f.mu.Unlock()
	if !same {
		w.WriteHeader(409)
		return
	}
	encode(w, result)
}

func (f *Fixture) complete(n *node, operation *nativeOperation, gate *Gate) {
	defer f.wg.Done()
	if gate != nil {
		defer close(gate.Finished)
		close(gate.Started)
		select {
		case <-gate.Release:
		case <-f.ctx.Done():
			return
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if operation.Permit.Action.Expected != n.fingerprint || operation.Permit.Action.Resource.Version != strconv.FormatUint(n.version, 10) {
		operation.Refused = true
		operation.Complete = true
		return
	}
	n.resource, n.attachment, n.fingerprint = operation.Resource, operation.Permit.Action.Attachment, operation.Permit.Action.Desired
	n.version++
	if operation.Permit.Action.Kind == cdn.SetTraffic {
		f.target = n.attachment.Profile.Reference
		if n.attachment.Mode == cdn.Direct {
			f.target = ""
		}
	}
	operation.Complete = true
}
