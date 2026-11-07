package sqliteprep

type Mode string

const (
	Fresh             Mode = "fresh"
	SupersedeLive     Mode = "supersede_live"
	SupersedeReverify Mode = "supersede_reverify"
	SupersedeCapture  Mode = "supersede_capture"
)

type Arm string

const (
	LTX      Arm = "ltx"
	Snapshot Arm = "snapshot"
	Empty    Arm = "allocated_empty"
)

type Stage string

const (
	Claimed           Stage = "claimed"
	WritersStopIntent Stage = "writers_stop_intent"
	WritersStopped    Stage = "writers_stopped"
	ReplicaStopIntent Stage = "replica_stop_intent"
	ReplicaStopped    Stage = "replica_stopped"
	CoverageIntent    Stage = "coverage_intent"
	CoverageUploaded  Stage = "coverage_uploaded"
	CoverageRetained  Stage = "coverage_retained"
	Verified          Stage = "verified"
	Prepared          Stage = "prepared"
)

func validModeArm(m Mode, a Arm) bool {
	switch m {
	case Fresh, SupersedeReverify:
		return a == LTX || a == Snapshot || a == Empty
	case SupersedeLive:
		return a == LTX
	case SupersedeCapture:
		return a == Snapshot || a == Empty
	default:
		return false
	}
}

// Next checks structural edges. ValidateHistory also enforces the immutable
// stop-intent requirement and the stage-specific payload and sequence numbers.
func Next(m Mode, a Arm, from, to Stage) bool {
	if !validModeArm(m, a) {
		return false
	}
	if from == Claimed {
		return to == WritersStopIntent && m != SupersedeReverify || to == WritersStopped && m != Fresh
	}
	if from == WritersStopIntent {
		return to == WritersStopped
	}
	if m == SupersedeReverify {
		return reverifyEdge(from, to)
	}
	if a == LTX {
		return liveEdge(from, to)
	}
	return snapshotEdge(from, to)
}
func reverifyEdge(from, to Stage) bool {
	switch from {
	case WritersStopped:
		return to == ReplicaStopped
	case ReplicaStopped:
		return to == CoverageRetained
	case CoverageRetained:
		return to == Verified
	case Verified:
		return to == Prepared
	default:
		return false
	}
}
func liveEdge(from, to Stage) bool {
	switch from {
	case WritersStopped:
		return to == CoverageUploaded
	case CoverageUploaded:
		return to == Verified
	case Verified:
		return to == ReplicaStopIntent
	case ReplicaStopIntent:
		return to == ReplicaStopped
	case ReplicaStopped:
		return to == Prepared
	default:
		return false
	}
}
func snapshotEdge(from, to Stage) bool {
	switch from {
	case WritersStopped:
		return to == ReplicaStopped
	case ReplicaStopped:
		return to == CoverageIntent
	case CoverageIntent:
		return to == CoverageUploaded
	case CoverageUploaded:
		return to == Verified
	case Verified:
		return to == Prepared
	default:
		return false
	}
}
func finalStage(i Intent) Stage {
	if i.Arm == LTX && i.Mode != SupersedeReverify {
		return ReplicaStopped
	}
	return Verified
}
