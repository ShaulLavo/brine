package spec

// UDPExposure identifies the operator-approved network scope for a direct UDP
// publication. The app may request the scope, but the operator policy grants it.
type UDPExposure string

const (
	UDPPublic  UDPExposure = "public"
	UDPTailnet UDPExposure = "tailnet"
)

type UDP struct {
	ContainerPort Port        `json:"container_port"`
	Exposure      UDPExposure `json:"exposure"`
}
