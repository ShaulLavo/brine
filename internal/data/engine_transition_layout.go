package data

import "encoding/json"

type EngineLocation struct {
	kind   string
	root   EngineDirectoryIdentity
	parent EngineDirectoryIdentity
	tree   EngineTreeSeal
}

func PresentEngineLocation(tree EngineTreeSeal) (EngineLocation, error) {
	if tree.Validate() != nil {
		return EngineLocation{}, ErrInvalid
	}
	return EngineLocation{kind: "present", root: tree.Root, parent: tree.Parent, tree: tree}, nil
}

func AbsentEngineLocation(root, parent EngineDirectoryIdentity) (EngineLocation, error) {
	if !root.valid() || !parent.valid() || root.Device != parent.Device {
		return EngineLocation{}, ErrInvalid
	}
	return EngineLocation{kind: "absent", root: root, parent: parent}, nil
}

func UnknownEngineLocation() EngineLocation { return EngineLocation{kind: "unknown"} }
func (l EngineLocation) Kind() string       { return l.kind }
func (l EngineLocation) Tree() (EngineTreeSeal, bool) {
	return l.tree, l.kind == "present"
}

func (l EngineLocation) at(tree EngineTreeSeal, parent EngineDirectoryIdentity) bool {
	return l.kind == "present" && l.root == tree.Root && l.parent == parent &&
		l.tree.Directory == tree.Directory && l.tree.ContentsSHA256 == tree.ContentsSHA256
}

func (l EngineLocation) absent(root, parent EngineDirectoryIdentity) bool {
	return l.kind == "absent" && l.root == root && l.parent == parent
}

type EngineLayoutObservation struct {
	Live      EngineLocation `json:"live"`
	Staged    EngineLocation `json:"staged"`
	Preserved EngineLocation `json:"preserved"`
}

type EngineLayoutState string

const (
	EngineLayoutPrepared  EngineLayoutState = "original_live_target_staged"
	EngineLayoutPreserved EngineLayoutState = "original_preserved_target_staged"
	EngineLayoutPromoted  EngineLayoutState = "target_live_original_preserved"
	EngineLayoutConflict  EngineLayoutState = "conflict"
)

// ClassifyEngineTransitionLayout compares recorded identities; it does no I/O.
func ClassifyEngineTransitionLayout(t EngineTransition, observed EngineLayoutObservation) EngineLayoutState {
	if t.hash == "" {
		return EngineLayoutConflict
	}
	c := t.common
	oldLive := observed.Live.at(c.OldTree, c.OldTree.Parent)
	newStaged := observed.Staged.at(c.TargetTree, c.TargetTree.Parent)
	oldPreserved := observed.Preserved.at(c.OldTree, c.PreservedParent)
	liveAbsent := observed.Live.absent(c.OldTree.Root, c.OldTree.Parent)
	stageAbsent := observed.Staged.absent(c.TargetTree.Root, c.TargetTree.Parent)
	preservedAbsent := observed.Preserved.absent(c.OldTree.Root, c.PreservedParent)
	if oldLive && newStaged && preservedAbsent {
		return EngineLayoutPrepared
	}
	if liveAbsent && newStaged && oldPreserved {
		return EngineLayoutPreserved
	}
	if observed.Live.at(c.TargetTree, c.OldTree.Parent) && stageAbsent && oldPreserved {
		return EngineLayoutPromoted
	}
	return EngineLayoutConflict
}

func (l EngineLocation) MarshalJSON() ([]byte, error) {
	switch l.kind {
	case "present":
		return json.Marshal(map[string]any{"kind": l.kind, "tree": l.tree})
	case "absent":
		return json.Marshal(map[string]any{"kind": l.kind, "root": l.root, "parent": l.parent})
	case "unknown":
		return json.Marshal(map[string]string{"kind": l.kind})
	default:
		return nil, ErrInvalid
	}
}

func (l *EngineLocation) UnmarshalJSON(raw []byte) error {
	fields, err := engineRecordFields(raw, []string{"kind"}, []string{"tree", "root", "parent"})
	if err != nil {
		return err
	}
	var kind string
	if json.Unmarshal(fields["kind"], &kind) != nil {
		return ErrInvalid
	}
	decoded, err := decodeEngineLocation(kind, fields)
	if err != nil {
		return err
	}
	*l = decoded
	return nil
}

func decodeEngineLocation(kind string, fields map[string]json.RawMessage) (EngineLocation, error) {
	switch kind {
	case "present":
		var tree EngineTreeSeal
		if len(fields) != 2 || fields["tree"] == nil || json.Unmarshal(fields["tree"], &tree) != nil {
			return EngineLocation{}, ErrInvalid
		}
		return PresentEngineLocation(tree)
	case "absent":
		var root, parent EngineDirectoryIdentity
		if len(fields) != 3 || fields["root"] == nil || fields["parent"] == nil ||
			json.Unmarshal(fields["root"], &root) != nil || json.Unmarshal(fields["parent"], &parent) != nil {
			return EngineLocation{}, ErrInvalid
		}
		return AbsentEngineLocation(root, parent)
	case "unknown":
		if len(fields) == 1 {
			return UnknownEngineLocation(), nil
		}
		return EngineLocation{}, ErrInvalid
	default:
		return EngineLocation{}, ErrInvalid
	}
}

func (o *EngineLayoutObservation) UnmarshalJSON(raw []byte) error {
	if engineRecord(raw, []string{"live", "staged", "preserved"}, nil) != nil {
		return ErrInvalid
	}
	type record EngineLayoutObservation
	var value record
	if json.Unmarshal(raw, &value) != nil {
		return ErrInvalid
	}
	*o = EngineLayoutObservation(value)
	return nil
}
