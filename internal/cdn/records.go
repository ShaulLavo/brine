package cdn

// Revision is committed only after all native effects settle and the selected
// public path passes qualification. App releases never select this history.
type Revision struct {
	Attachment Attachment     `json:"attachment"`
	Plan       PlanID         `json:"plan"`
	Operation  OperationID    `json:"operation"`
	Proof      PublicProof    `json:"proof"`
	Receipts   []OwnedReceipt `json:"receipts"`
	Readback   []Observation  `json:"readback"`
}

type Status struct {
	Plan      Plan          `json:"plan"`
	Operation OperationID   `json:"operation"`
	State     string        `json:"state"`
	Effects   []Effect      `json:"effects"`
	Proof     *PublicProof  `json:"proof"`
	Readback  []Observation `json:"readback"`
}
