package cloudflare

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"

	"github.com/ShaulLavo/brine/internal/cdn"
	"github.com/ShaulLavo/brine/internal/strictjson"
)

const compatibilityDate = "2026-10-17"

type observability struct {
	Enabled           bool `json:"enabled"`
	RedactQueryString bool `json:"redact_query_string"`
	Logs              struct {
		Enabled        bool `json:"enabled"`
		InvocationLogs bool `json:"invocation_logs"`
	} `json:"logs"`
	Traces struct {
		Enabled bool `json:"enabled"`
	} `json:"traces"`
}

type scriptSettings struct {
	Tags               []string                      `json:"tags"`
	Bindings           []json.RawMessage             `json:"bindings"`
	CompatibilityDate  string                        `json:"compatibility_date"`
	CompatibilityFlags []string                      `json:"compatibility_flags"`
	Observability      observability                 `json:"observability"`
	CacheOptions       *nativeCacheOptions           `json:"cache_options"`
	Exports            map[string]nativeWorkerExport `json:"exports,omitempty"`
	TailConsumers      []json.RawMessage             `json:"tail_consumers"`
}

type nativeCacheOptions struct {
	Enabled           bool `json:"enabled"`
	CrossVersionCache bool `json:"cross_version_cache"`
}

func (c *nativeCacheOptions) UnmarshalJSON(raw []byte) error {
	fields, err := strictjson.Object(raw, "enabled", "cross_version_cache")
	if err != nil {
		return cdn.ErrInvalid
	}
	enabled, err := strictjson.Value[bool](fields["enabled"])
	if err != nil {
		return cdn.ErrInvalid
	}
	cross, err := strictjson.Value[bool](fields["cross_version_cache"])
	if err != nil {
		return cdn.ErrInvalid
	}
	c.Enabled, c.CrossVersionCache = enabled, cross
	return nil
}

type nativeEntrypointCache struct {
	Enabled bool `json:"enabled"`
}

func (c *nativeEntrypointCache) UnmarshalJSON(raw []byte) error {
	fields, err := strictjson.Object(raw, "enabled")
	if err != nil {
		return cdn.ErrInvalid
	}
	enabled, err := strictjson.Value[bool](fields["enabled"])
	if err != nil {
		return cdn.ErrInvalid
	}
	c.Enabled = enabled
	return nil
}

type nativeWorkerExport struct {
	Type  string                 `json:"type"`
	State string                 `json:"state,omitempty"`
	Cache *nativeEntrypointCache `json:"cache"`
}

func (e *nativeWorkerExport) UnmarshalJSON(raw []byte) error {
	fields, err := strictjson.Object(raw, "type", "state", "cache")
	if err != nil {
		fields, err = strictjson.Object(raw, "type", "cache")
	}
	if err != nil {
		return cdn.ErrInvalid
	}
	typeName, err := strictjson.Value[string](fields["type"])
	if err != nil {
		return cdn.ErrInvalid
	}
	state := "created"
	if rawState, ok := fields["state"]; ok {
		state, err = strictjson.Value[string](rawState)
		if err != nil {
			return cdn.ErrInvalid
		}
	}
	var cache nativeEntrypointCache
	if json.Unmarshal(fields["cache"], &cache) != nil || string(fields["cache"]) == "null" {
		return cdn.ErrInvalid
	}
	*e = nativeWorkerExport{Type: typeName, State: state, Cache: &cache}
	return nil
}

type nativeScriptMetadata struct {
	ID           string                        `json:"id"`
	CacheOptions *nativeCacheOptions           `json:"cache_options"`
	Exports      map[string]nativeWorkerExport `json:"exports"`
	HasAssets    *bool                         `json:"has_assets"`
	HasModules   *bool                         `json:"has_modules"`
}

type scriptConfiguration struct {
	Source   cdn.Fingerprint
	Settings scriptSettings
}

type exposure struct {
	Enabled         bool `json:"enabled"`
	PreviewsEnabled bool `json:"previews_enabled"`
}

func (e *exposure) UnmarshalJSON(raw []byte) error {
	var fields struct {
		Enabled         *bool `json:"enabled"`
		PreviewsEnabled *bool `json:"previews_enabled"`
	}
	if json.Unmarshal(raw, &fields) != nil || fields.Enabled == nil || fields.PreviewsEnabled == nil {
		return cdn.ErrInvalid
	}
	e.Enabled, e.PreviewsEnabled = *fields.Enabled, *fields.PreviewsEnabled
	return nil
}

type route struct {
	ID      string `json:"id"`
	Pattern string `json:"pattern"`
	Script  string `json:"script"`
}

func (d *Adapter) observeEdge(ctx context.Context, a cdn.Attachment) (cdn.Observation, error) {
	name := workerName(d.client.profile, a)
	script, err := d.observeScript(ctx, a, name)
	if err != nil {
		return cdn.Observation{}, err
	}
	public := missing(cdn.EdgeExposure, name, a)
	if script.ID != "" {
		public, err = d.observeExposure(ctx, a, name)
		if err != nil {
			return cdn.Observation{}, err
		}
	}
	trafficRoute, err := d.observeRoute(ctx, a)
	if err != nil {
		return cdn.Observation{}, err
	}
	return observation(d.scope, []cdn.OwnedResource{script, public, trafficRoute}), nil
}

func (d *Adapter) observeScript(ctx context.Context, a cdn.Attachment, name string) (cdn.OwnedResource, error) {
	var settings scriptSettings
	err := d.client.get(ctx, d.scriptPath(name)+"/settings", ReadWorkers, &settings)
	if isMissing(err) {
		return missing(cdn.EdgeConfiguration, name, a), nil
	}
	if err != nil {
		return cdn.OwnedResource{}, err
	}
	if !slices.Contains(settings.Tags, ownerTag(a)) {
		return cdn.OwnedResource{}, &APIError{Kind: OwnershipConflict, Permission: ReadWorkers}
	}
	if len(settings.Bindings) != 0 {
		return cdn.OwnedResource{}, &APIError{Kind: OwnershipConflict, Permission: ReadWorkers}
	}
	if len(settings.TailConsumers) != 0 {
		return cdn.OwnedResource{}, &APIError{Kind: OwnershipConflict, Permission: ReadWorkers}
	}
	if err = d.verifyDispatch(ctx, name, &settings); err != nil {
		return cdn.OwnedResource{}, err
	}
	if settings.Bindings == nil {
		settings.Bindings = make([]json.RawMessage, 0)
	}
	if settings.CompatibilityFlags == nil {
		settings.CompatibilityFlags = make([]string, 0)
	}
	if settings.TailConsumers == nil {
		settings.TailConsumers = make([]json.RawMessage, 0)
	}
	response, err := d.client.call(ctx, http.MethodGet, d.scriptPath(name)+"/content/v2", "", nil, ReadWorkers)
	if err != nil {
		return cdn.OwnedResource{}, err
	}
	source, err := moduleSource(response)
	if err != nil {
		return cdn.OwnedResource{}, err
	}
	slices.Sort(settings.Tags)
	return cdn.OwnedResource{Kind: cdn.EdgeConfiguration, ID: name, ExactName: name, Owner: a.Owner,
		Fingerprint: fingerprint(scriptConfiguration{cdn.Hash(source), settings})}, nil
}

func (d *Adapter) verifyDispatch(ctx context.Context, name string, settings *scriptSettings) error {
	metadata, err := d.scriptMetadata(ctx, name)
	if err != nil {
		return err
	}
	if settings.CacheOptions == nil || metadata.CacheOptions == nil || metadata.HasAssets == nil || metadata.HasModules == nil || len(metadata.Exports) != 1 {
		return &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
	}
	if *settings.CacheOptions != *metadata.CacheOptions || settings.CacheOptions.Enabled || settings.CacheOptions.CrossVersionCache || *metadata.HasAssets || !*metadata.HasModules {
		return &APIError{Kind: OwnershipConflict, Permission: ReadWorkers}
	}
	entry, ok := metadata.Exports["default"]
	if !ok || entry.Cache == nil {
		return &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
	}
	if entry.Type != "worker" || entry.Cache.Enabled || entry.State != "" && entry.State != "created" {
		return &APIError{Kind: OwnershipConflict, Permission: ReadWorkers}
	}
	// The native Worker export schema defines an omitted state as created.
	entry.State = "created"
	metadata.Exports["default"] = entry
	if settings.Exports != nil && fingerprint(settings.Exports) != fingerprint(metadata.Exports) {
		return &APIError{Kind: OwnershipConflict, Permission: ReadWorkers}
	}
	settings.Exports = metadata.Exports
	return nil
}

func (d *Adapter) scriptMetadata(ctx context.Context, name string) (nativeScriptMetadata, error) {
	response, err := d.client.call(ctx, http.MethodGet, nativePath("accounts", d.client.profile.document.AccountID, "workers", "scripts"), "", nil, ReadWorkers)
	if err != nil {
		return nativeScriptMetadata{}, err
	}
	var rows []json.RawMessage
	if err = decodeEnvelope(response, ReadWorkers, &rows); err != nil {
		return nativeScriptMetadata{}, err
	}
	var document envelope
	if json.Unmarshal(response.body, &document) != nil || len(rows) > 1000 || document.ResultInfo.TotalPages > 1 || document.ResultInfo.TotalCount > len(rows) {
		return nativeScriptMetadata{}, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
	}
	return exactScriptMetadata(rows, name)
}

func exactScriptMetadata(rows []json.RawMessage, name string) (nativeScriptMetadata, error) {
	var found *nativeScriptMetadata
	for _, row := range rows {
		var header struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(row, &header) != nil {
			return nativeScriptMetadata{}, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
		}
		if header.ID != name {
			continue
		}
		if found != nil {
			return nativeScriptMetadata{}, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
		}
		var metadata nativeScriptMetadata
		if json.Unmarshal(row, &metadata) != nil {
			return nativeScriptMetadata{}, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
		}
		found = &metadata
	}
	if found == nil {
		return nativeScriptMetadata{}, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
	}
	return *found, nil
}

func moduleSource(response reply) ([]byte, error) {
	contentType, parameters, err := mime.ParseMediaType(response.headers.Get("Content-Type"))
	if err != nil {
		return nil, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
	}
	if contentType == "application/javascript" || contentType == "application/javascript+module" || contentType == "text/javascript" {
		return response.body, nil
	}
	if !strings.HasPrefix(contentType, "multipart/") {
		return nil, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
	}
	reader := multipart.NewReader(strings.NewReader(string(response.body)), parameters["boundary"])
	return multipartSource(reader)
}

func multipartSource(reader *multipart.Reader) ([]byte, error) {
	var source []byte
	metadataSeen := false
	for index := 0; index < 3; index++ {
		part, err := reader.NextPart()
		if err == io.EOF && len(source) > 0 {
			return source, nil
		}
		if err != nil || index == 2 {
			return nil, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
		}
		raw, err := io.ReadAll(io.LimitReader(part, responseLimit+1))
		if err != nil || len(raw) > responseLimit {
			return nil, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
		}
		if part.FormName() == "metadata" && !metadataSeen && validModuleMetadata(raw) {
			metadataSeen = true
			continue
		}
		if part.FormName() != "worker.mjs" || source != nil {
			return nil, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
		}
		source = raw
	}
	return nil, &APIError{Kind: InvalidResponse, Permission: ReadWorkers}
}

func validModuleMetadata(raw []byte) bool {
	var metadata struct {
		MainModule string `json:"main_module"`
	}
	return json.Unmarshal(raw, &metadata) == nil && metadata.MainModule == "worker.mjs"
}

func (d *Adapter) observeExposure(ctx context.Context, a cdn.Attachment, name string) (cdn.OwnedResource, error) {
	var state exposure
	if err := d.client.get(ctx, d.scriptPath(name)+"/subdomain", ReadWorkers, &state); err != nil {
		return cdn.OwnedResource{}, err
	}
	return cdn.OwnedResource{Kind: cdn.EdgeExposure, ID: name, ExactName: name, Owner: a.Owner, Fingerprint: fingerprint(state)}, nil
}

func (d *Adapter) observeRoute(ctx context.Context, a cdn.Attachment) (cdn.OwnedResource, error) {
	response, err := d.client.call(ctx, http.MethodGet, d.routePath(), "", nil, ReadRoutes)
	if err != nil {
		return cdn.OwnedResource{}, err
	}
	var routes []route
	if err = decodeEnvelope(response, ReadRoutes, &routes); err != nil {
		return cdn.OwnedResource{}, err
	}
	var document envelope
	if json.Unmarshal(response.body, &document) != nil || len(routes) > 1000 || document.ResultInfo.TotalPages > 1 || document.ResultInfo.TotalCount > len(routes) {
		return cdn.OwnedResource{}, &APIError{Kind: InvalidResponse, Permission: ReadRoutes}
	}
	return d.ownedRoute(ctx, a, routes)
}

func (d *Adapter) ownedRoute(ctx context.Context, a cdn.Attachment, routes []route) (cdn.OwnedResource, error) {
	pattern := d.client.profile.document.RoutePattern
	found := missing(cdn.EdgeRoute, pattern, a)
	for _, candidate := range routes {
		if candidate.Pattern != pattern && candidate.Script == workerName(d.client.profile, a) {
			return cdn.OwnedResource{}, &APIError{Kind: OwnershipConflict, Permission: ReadRoutes}
		}
		if overlapsRoute(candidate.Pattern, pattern) {
			return cdn.OwnedResource{}, &APIError{Kind: OwnershipConflict, Permission: ReadRoutes}
		}
		if candidate.Pattern != pattern {
			continue
		}
		if found.ID != "" || !nativeID.MatchString(candidate.ID) || !strings.HasPrefix(candidate.Script, d.client.profile.document.WorkerPrefix+"-") {
			return cdn.OwnedResource{}, &APIError{Kind: OwnershipConflict, Permission: ReadRoutes}
		}
		if err := d.verifyRouteOwner(ctx, a, candidate.Script); err != nil {
			return cdn.OwnedResource{}, err
		}
		found = cdn.OwnedResource{Kind: cdn.EdgeRoute, ID: candidate.ID, ExactName: pattern, Owner: a.Owner, Fingerprint: fingerprint(struct{ Pattern, Script string }{pattern, candidate.Script})}
	}
	return found, nil
}

func overlapsRoute(candidate, owned string) bool {
	if candidate == owned || strings.HasPrefix(candidate, "http://") {
		return false
	}
	other := strings.TrimPrefix(candidate, "https://")
	prefix := strings.TrimSuffix(strings.TrimPrefix(owned, "https://"), "*")
	return strings.HasPrefix(other, prefix)
}

func (d *Adapter) verifyRouteOwner(ctx context.Context, a cdn.Attachment, name string) error {
	var settings scriptSettings
	if err := d.client.get(ctx, d.scriptPath(name)+"/settings", ReadWorkers, &settings); err != nil {
		return err
	}
	if !slices.Contains(settings.Tags, ownerTag(a)) {
		return &APIError{Kind: OwnershipConflict, Permission: ReadRoutes}
	}
	return nil
}
