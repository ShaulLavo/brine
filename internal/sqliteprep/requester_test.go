package sqliteprep

import "testing"

// Real operations carry the restricted deploy:<sha256> channel identity.
func TestTaskRequesterIsRestrictedChannelIdentity(t *testing.T) {
	task := fixtureIntent().Task
	if task.Validate() != nil {
		t.Fatal("real requester refused")
	}
	for _, requester := range []string{"owner", "deploy:" + hash("A"), "deploy:" + hash("a")[:63], "operator:" + hash("a")} {
		task.Requester = requester
		if task.Validate() == nil {
			t.Fatalf("requester %q accepted", requester)
		}
	}
}
