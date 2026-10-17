package cloudflare

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/textproto"

	"github.com/ShaulLavo/brine/internal/cdn"
)

func (d *Adapter) Execute(ctx context.Context, permit cdn.Permit) (cdn.EffectResult, error) {
	if err := d.admitPermit(permit); err != nil {
		return nil, err
	}
	if d.traffic {
		return d.executeTraffic(ctx, permit)
	}
	current, err := d.Observe(ctx, permit.Action.Attachment)
	if err != nil {
		return refusal(permit, err), nil
	}
	if err = d.readyForStep(permit.Action.Attachment, cdn.PlannedStep{Kind: permit.Action.Kind}, current); err != nil {
		return cdn.Refused{Effect: permit.Effect, Code: "native_prerequisite_changed"}, nil //nolint:nilerr // the typed effect result records the refusal.
	}
	current = selected(current, permit.Action.Resource.Kind)
	if !matchesBefore(permit.Action, current) {
		return cdn.Refused{Effect: permit.Effect, Code: "native_state_changed"}, nil
	}
	if permit.Action.Expected == permit.Action.Desired {
		return cdn.Acknowledged{Effect: permit.Effect, Receipt: fingerprint(current), Resources: current.Resources}, nil
	}
	response, resource, err := d.edgeMutation(ctx, permit)
	if err != nil {
		return mutationFailure(permit, err), nil
	}
	return cdn.Acknowledged{Effect: permit.Effect, Receipt: cdn.Hash(response.body), Resources: []cdn.OwnedResource{resource}}, nil
}

func matchesBefore(action cdn.Action, current cdn.Observation) bool {
	return len(current.Resources) == 1 && current.Fingerprint == action.Expected && current.Resources[0].ID == action.Resource.ID && current.Resources[0].Version == action.Resource.Version && current.Resources[0].Owner == action.Resource.Owner
}

func refusal(permit cdn.Permit, err error) cdn.Refused {
	code := "native_observation_unavailable"
	var api *APIError
	if errors.Is(err, ErrCredential) {
		code = "local_credential_unavailable"
	}
	if errors.As(err, &api) {
		code = string(api.Kind) + ":" + string(api.Permission)
	}
	return cdn.Refused{Effect: permit.Effect, Code: code}
}

func mutationFailure(permit cdn.Permit, err error) cdn.EffectResult {
	var api *APIError
	if errors.Is(err, ErrCredential) || errors.As(err, &api) && api.Kind == PermissionDenied {
		return refusal(permit, err)
	}
	return cdn.Uncertain{Effect: permit.Effect, Code: "native_write_unsettled"}
}

func (d *Adapter) edgeMutation(ctx context.Context, permit cdn.Permit) (reply, cdn.OwnedResource, error) {
	action := permit.Action
	resource, err := d.desiredResource(action.Attachment, action.Resource.Kind)
	if err != nil {
		return reply{}, resource, err
	}
	method, path, contentType, body, permission, err := d.edgeRequest(action)
	if err != nil {
		return reply{}, resource, err
	}
	response, err := d.client.call(ctx, method, path, contentType, bytes.NewReader(body), permission)
	if err != nil {
		return response, resource, err
	}
	return d.edgeAcknowledgement(action, response, resource)
}

func (d *Adapter) edgeRequest(action cdn.Action) (string, string, string, []byte, Permission, error) {
	name := workerName(d.client.profile, action.Attachment)
	switch action.Kind {
	case cdn.PutEdge:
		body, contentType, err := scriptUpload(action.Attachment)
		return http.MethodPut, d.scriptPath(name), contentType, body, WriteWorkers, err
	case cdn.SetEdgeExposure:
		return http.MethodPost, d.scriptPath(name) + "/subdomain", "application/json", []byte(`{"enabled":false,"previews_enabled":false}`), WriteWorkers, nil
	case cdn.PutEdgeRoute:
		body, _ := json.Marshal(struct {
			Pattern string `json:"pattern"`
			Script  string `json:"script"`
		}{d.client.profile.document.RoutePattern, name})
		if action.Resource.ID == "" {
			return http.MethodPost, d.routePath(), "application/json", body, WriteRoutes, nil
		}
		return http.MethodPut, d.routePath() + "/" + action.Resource.ID, "application/json", body, WriteRoutes, nil
	case cdn.RemoveEdgeRoute:
		return http.MethodDelete, d.routePath() + "/" + action.Resource.ID, "", nil, WriteRoutes, nil
	case cdn.RemoveEdge:
		return http.MethodDelete, d.scriptPath(name), "", nil, WriteWorkers, nil
	default:
		return "", "", "", nil, "", cdn.ErrInvalid
	}
}

func scriptUpload(a cdn.Attachment) ([]byte, string, error) {
	source, err := WorkerSource(a)
	if err != nil {
		return nil, "", err
	}
	metadata, err := json.Marshal(struct {
		MainModule string `json:"main_module"`
		KeepAssets bool   `json:"keep_assets"`
		scriptSettings
	}{"worker.mjs", false, desiredSettings(a, source)})
	if err != nil {
		return nil, "", err
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	if err = uploadPart(writer, "metadata", "application/json", metadata); err != nil {
		return nil, "", err
	}
	if err = uploadPart(writer, "worker.mjs", "application/javascript+module", source); err != nil {
		return nil, "", err
	}
	if err = writer.Close(); err != nil {
		return nil, "", err
	}
	return buffer.Bytes(), writer.FormDataContentType(), nil
}

func uploadPart(writer *multipart.Writer, name, contentType string, value []byte) error {
	headers := textproto.MIMEHeader{}
	headers.Set("Content-Disposition", `form-data; name="`+name+`"`)
	if name == "worker.mjs" {
		headers.Set("Content-Disposition", `form-data; name="worker.mjs"; filename="worker.mjs"`)
	}
	headers.Set("Content-Type", contentType)
	part, err := writer.CreatePart(headers)
	if err != nil {
		return err
	}
	_, err = part.Write(value)
	return err
}

func (d *Adapter) edgeAcknowledgement(action cdn.Action, response reply, resource cdn.OwnedResource) (reply, cdn.OwnedResource, error) {
	var body envelope
	if json.Unmarshal(response.body, &body) != nil || !body.Success {
		return response, resource, &APIError{Kind: WriteUnknown}
	}
	switch action.Kind {
	case cdn.PutEdge:
		var script struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(body.Result, &script) != nil || script.ID != resource.ID {
			return response, resource, &APIError{Kind: WriteUnknown}
		}
	case cdn.SetEdgeExposure:
		var actual exposure
		if json.Unmarshal(body.Result, &actual) != nil || actual.Enabled || actual.PreviewsEnabled {
			return response, resource, &APIError{Kind: WriteUnknown}
		}
	case cdn.PutEdgeRoute:
		var actual route
		if json.Unmarshal(body.Result, &actual) != nil || !nativeID.MatchString(actual.ID) || actual.Pattern != resource.ExactName || actual.Script != workerName(d.client.profile, action.Attachment) {
			return response, resource, &APIError{Kind: WriteUnknown}
		}
		resource.ID = actual.ID
	case cdn.RemoveEdge, cdn.RemoveEdgeRoute:
		if action.Resource.ID == "" {
			return response, resource, cdn.ErrInvalid
		}
	default:
		return response, resource, cdn.ErrInvalid
	}
	return response, resource, nil
}

func (d *Adapter) executeTraffic(ctx context.Context, permit cdn.Permit) (cdn.EffectResult, error) {
	a := permit.Action.Attachment
	before, err := d.readTraffic(ctx, a)
	if err != nil {
		return refusal(permit, err), nil
	}
	if trafficFingerprint(before) != permit.Action.Expected || permit.Action.Resource.ID != before.ID || permit.Action.Resource.Version != before.ModifiedOn {
		return cdn.Refused{Effect: permit.Effect, Code: "native_state_changed"}, nil
	}
	if permit.Action.Expected == permit.Action.Desired {
		return cdn.Acknowledged{Effect: permit.Effect, Receipt: fingerprint(before), Resources: []cdn.OwnedResource{permit.Action.Resource}}, nil
	}
	next := before
	next.Proxied = a.Mode == cdn.Enabled
	if next.Proxied {
		next.TTL = 1
	}
	if trafficFingerprint(next) != permit.Action.Desired {
		return nil, cdn.ErrInvalid
	}
	body, _ := json.Marshal(struct {
		Proxied bool `json:"proxied"`
	}{next.Proxied})
	response, err := d.client.call(ctx, http.MethodPatch, d.dnsPath(), "application/json", bytes.NewReader(body), WriteDNS)
	if err != nil {
		return mutationFailure(permit, err), nil
	}
	var actual dnsRecord
	if decodeEnvelope(response, WriteDNS, &actual) != nil || actual.ID != before.ID || trafficFingerprint(actual) != permit.Action.Desired {
		return cdn.Uncertain{Effect: permit.Effect, Code: "native_write_unsettled"}, nil //nolint:nilerr // the typed effect result keeps this mutation unsettled.
	}
	resource := permit.Action.Resource
	resource.Version, resource.Fingerprint = actual.ModifiedOn, trafficFingerprint(actual)
	return cdn.Acknowledged{Effect: permit.Effect, Receipt: cdn.Hash(response.body), Resources: []cdn.OwnedResource{resource}}, nil
}
