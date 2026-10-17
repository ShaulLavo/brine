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
	Reference   ProfileRef  `json:"reference"`
	Driver      string      `json:"driver"`
	Fingerprint Fingerprint `json:"fingerprint"`
}

type Origin struct {
	Address string `json:"address"`
	Host    string `json:"host"`
	TLSName string `json:"tls_name"`
}

type CachePolicy struct {
	Prefix     string   `json:"prefix"`
	Extensions []string `json:"extensions"`
	TTLSeconds uint32   `json:"ttl_seconds"`
}

type Binding struct {
	Reference        string           `json:"reference"`
	App              string           `json:"app"`
	Target           Fingerprint      `json:"target"`
	Hostname         string           `json:"hostname"`
	Origin           Origin           `json:"origin"`
	Cache            CachePolicy      `json:"cache"`
	AllowedEdges     []ProfileScope   `json:"allowed_edges"`
	Traffic          ProfileScope     `json:"traffic"`
	TrafficAuthority TrafficAuthority `json:"traffic_authority"`
}

type TrafficAuthority struct {
	ResourceID string      `json:"resource_id"`
	Hostname   string      `json:"hostname"`
	Before     Fingerprint `json:"before"`
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
	Revision       RevisionID  `json:"revision"`
	OriginHealthy  bool        `json:"origin_healthy"`
	EdgeHealthy    bool        `json:"edge_healthy"`
	FullGETs       uint32      `json:"full_gets"`
	HashesVerified bool        `json:"hashes_verified"`
	LaterHit       bool        `json:"later_hit"`
	Evidence       Fingerprint `json:"evidence"`
}

type EdgeDriver interface {
	Observe(context.Context, Attachment) (Observation, error)
	Actions(Attachment, Observation) ([]Action, error)
	Execute(context.Context, Permit) (EffectResult, error)
	Inspect(context.Context, Permit) (Inspection, error)
	Probe(context.Context, Attachment) (PublicProof, error)
}
type TrafficDriver interface {
	Observe(context.Context, Attachment) (Observation, error)
	Actions(Attachment, Observation) ([]Action, error)
	Execute(context.Context, Permit) (EffectResult, error)
	Inspect(context.Context, Permit) (Inspection, error)
}

type Work interface{ work() }
type NeedEffect struct {
	Permit Permit `json:"permit"`
}
type NeedObservation struct {
	Permit Permit `json:"permit"`
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
