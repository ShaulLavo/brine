package data

// ArchiveDeletionParent binds absence adoption to the namespace reviewed before
// deletion. A replaced archives directory cannot hide an undeleted old bundle.
type ArchiveDeletionParent struct {
	Root   PersistentRoot `json:"root"`
	Device uint64         `json:"device"`
	Inode  uint64         `json:"inode"`
}

func (p ArchiveDeletionParent) Valid(t ArchiveTree) bool {
	return p.Root == t.Root && p.Device == t.RootDevice && p.Inode != 0
}
