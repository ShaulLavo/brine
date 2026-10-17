// Package cdn models host-journaled CDN attachments and operator-side effects.
package cdn

import (
	"context"
	"errors"
	"time"
)

type AttachmentID string
type RevisionID string
type OperationID string
type EffectID string
type ClaimID string
type PlanID string
type ProfileRef string
type Fingerprint string

var ErrInvalid = errors.New("cdn: invalid input")
var ErrConflict = errors.New("cdn: conflicting state")

type Owner struct {
	Target      Fingerprint  `json:"target"`
	App         string       `json:"app"`
	Incarnation string       `json:"incarnation"`
	Attachment  AttachmentID `json:"attachment"`
}

type ProfileScope struct {
	Reference   ProfileRef  `toml:"reference" json:"reference"`
	Driver      string      `toml:"driver" json:"driver"`
	Fingerprint Fingerprint `toml:"fingerprint" json:"fingerprint"`
}

type Origin struct {
	Address string `toml:"address" json:"address"`
	Host    string `toml:"host" json:"host"`
	TLSName string `toml:"tls_name" json:"tls_name"`
}

type CachePolicy struct {
	Prefix     string   `toml:"prefix" json:"prefix"`
	Extensions []string `toml:"extensions" json:"extensions"`
	TTLSeconds uint32   `toml:"ttl_seconds" json:"ttl_seconds"`
}

type Binding struct {
	Reference        string           `toml:"reference" json:"reference"`
	App              string           `toml:"app" json:"app"`
	Target           Fingerprint      `toml:"target" json:"target"`
	Hostname         string           `toml:"hostname" json:"hostname"`
	Origin           Origin           `toml:"origin" json:"origin"`
	Cache            CachePolicy      `toml:"cache" json:"cache"`
	AllowedEdges     []ProfileScope   `toml:"allowed_edges" json:"allowed_edges"`
	Traffic          ProfileScope     `toml:"traffic" json:"traffic"`
	TrafficAuthority TrafficAuthority `toml:"traffic_authority" json:"traffic_authority"`
}

type TrafficAuthority struct {
	ResourceID string      `toml:"resource_id" json:"resource_id"`
	Hostname   string      `toml:"hostname" json:"hostname"`
	Before     Fingerprint `toml:"before" json:"before"`
}

type Mode string

const (
	Enabled Mode = "enabled"
	Direct  Mode = "direct"
)

type Attachment struct {
	Mode          Mode         `json:"mode"`
	Owner         Owner        `json:"owner"`
	Binding       Binding      `json:"binding"`
	Profile       ProfileScope `json:"profile"`
	Revision      RevisionID   `json:"revision"`
	SourceRelease string       `json:"source_release"`
}

type ResourceKind string

const (
	EdgeConfiguration ResourceKind = "edge_configuration"
	EdgeExposure      ResourceKind = "edge_exposure"
	EdgeRoute         ResourceKind = "edge_route"
	HostnameTraffic   ResourceKind = "hostname_traffic"
)

type OwnedResource struct {
	Kind        ResourceKind `json:"kind"`
	ID          string       `json:"id"`
	ExactName   string       `json:"exact_name"`
	Owner       Owner        `json:"owner"`
	Version     string       `json:"version"`
	Fingerprint Fingerprint  `json:"fingerprint"`
}

type ActionKind string

const (
	PutEdge         ActionKind = "put_edge"
	SetEdgeExposure ActionKind = "set_edge_exposure"
	PutEdgeRoute    ActionKind = "put_edge_route"
	SetTraffic      ActionKind = "set_traffic"
	RemoveEdgeRoute ActionKind = "remove_edge_route"
	RemoveEdge      ActionKind = "remove_edge"
)

// Each action authorizes exactly one native provider mutation.
type Action struct {
	Kind       ActionKind    `json:"kind"`
	Attachment Attachment    `json:"attachment"`
	Profile    ProfileScope  `json:"profile"`
	Resource   OwnedResource `json:"resource"`
	Expected   Fingerprint   `json:"expected"`
	Desired    Fingerprint   `json:"desired"`
}

type Permit struct {
	Operation OperationID `json:"operation"`
	Effect    EffectID    `json:"effect"`
	Claim     ClaimID     `json:"claim"`
	Action    Action      `json:"action"`
}

type Observation struct {
	Profile     ProfileScope    `json:"profile"`
	Resources   []OwnedResource `json:"resources"`
	Fingerprint Fingerprint     `json:"fingerprint"`
	ObservedAt  time.Time       `json:"observed_at"`
}

type EffectResult interface{ effectResult() }
type Acknowledged struct {
	Effect    EffectID        `json:"effect"`
	Receipt   Fingerprint     `json:"receipt"`
	Resources []OwnedResource `json:"resources"`
}
type Refused struct {
	Effect EffectID `json:"effect"`
	Code   string   `json:"code"`
}
type Uncertain struct {
	Effect EffectID `json:"effect"`
	Code   string   `json:"code"`
}

func (Acknowledged) effectResult() {}
func (Refused) effectResult()      {}
func (Uncertain) effectResult()    {}

type Relation string

const (
	Before   Relation = "before"
	Desired  Relation = "desired"
	Absent   Relation = "absent"
	Diverged Relation = "diverged"
	Unknown  Relation = "unknown"
)

type Inspection interface{ inspection() }

// Observed state alone never proves an uncertain write has finished.
type Observed struct {
	Effect   EffectID    `json:"effect"`
	Current  Observation `json:"current"`
	Relation Relation    `json:"relation"`
}

// Completed requires native conditional/idempotency completion evidence.
type Completed struct {
	Effect   EffectID    `json:"effect"`
	Current  Observation `json:"current"`
	Evidence Fingerprint `json:"evidence"`
}

func (Observed) inspection()  {}
func (Completed) inspection() {}

type PublicProof struct {
	Method         ProofMethod  `json:"method"`
	Sample         AssetSample  `json:"sample"`
	Source         ProfileScope `json:"source"`
	ObservedAt     time.Time    `json:"observed_at"`
	Revision       RevisionID   `json:"revision"`
	OriginHealthy  bool         `json:"origin_healthy"`
	EdgeHealthy    bool         `json:"edge_healthy"`
	FullGETs       uint32       `json:"full_gets"`
	HashesVerified bool         `json:"hashes_verified"`
	LaterHit       bool         `json:"later_hit"`
	Evidence       Fingerprint  `json:"evidence"`
}

type ProofMethod string

const (
	OperatorHTTP   ProofMethod = "operator_http"
	BrowserNetwork ProofMethod = "browser_network"
)

type AssetSample struct {
	Path   string      `json:"path"`
	Digest Fingerprint `json:"digest"`
}

type Qualification struct {
	Attachment Attachment  `json:"attachment"`
	Sample     AssetSample `json:"sample"`
}

type EdgeDriver interface {
	Observe(context.Context, Attachment) (Observation, error)
	Actions(Attachment, Observation) ([]PlannedStep, error)
	Seal(context.Context, Attachment, PlannedStep, OwnedReceipt) (SealedAction, error)
	Execute(context.Context, Permit) (EffectResult, error)
	Inspect(context.Context, Permit) (Inspection, error)
	Probe(context.Context, Qualification) (PublicProof, error)
}
type TrafficDriver interface {
	Observe(context.Context, Attachment) (Observation, error)
	Actions(Attachment, Observation) ([]PlannedStep, error)
	Seal(context.Context, Attachment, PlannedStep, OwnedReceipt) (SealedAction, error)
	Execute(context.Context, Permit) (EffectResult, error)
	Inspect(context.Context, Permit) (Inspection, error)
}

type Work interface{ work() }
type NeedEffect struct {
	Permit Permit `json:"permit"`
}
type NeedObservation struct {
	Request ObservationRequest `json:"request"`
}
type Waiting struct {
	Code string `json:"code"`
}
type Finished struct {
	Operation OperationID `json:"operation"`
	Revision  RevisionID  `json:"revision"`
	Succeeded bool        `json:"succeeded"`
}

func (NeedEffect) work()      {}
func (NeedObservation) work() {}
func (Waiting) work()         {}
func (Finished) work()        {}
