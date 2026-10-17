package ops

// CDNPayload contains fixed progress and digests, never provider bodies or credentials.
type CDNPayload struct {
	PlanID   string `json:"plan_id"`
	Effect   string `json:"effect"`
	Stage    string `json:"stage"`
	Evidence string `json:"evidence"`
}
