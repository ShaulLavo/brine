package replication

// Artifacts are the immutable committed config and rendered ordinary user unit.
// The caller binds Service to its recorded hash and RenderReplica output before
// publication, and journals the effect under the host mutation lock.
type Artifacts struct {
	Binding                               Binding
	Config, Service                       []byte
	ConfigPath, LifetimeLock, ServicePath string
}
