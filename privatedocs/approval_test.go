package privatedocs_test

import (
	"errors"
	"path/filepath"
	"testing"

	"idp.local/reporting/privatedocs"
	r "idp.local/reporting/reporting"
)

// S12-C: in a shared space a version records who saved it, and its publication is another person's: the saver, or a version without a
// saver, is refused; a withdrawal is anyone's; one's own designs (no approver) publish as before.
func TestPublicationOfASharedVersionIsAnotherPersonsApproval(t *testing.T) {
	s := openString(t, filepath.Join(t.TempDir(), "d"), 0)
	defer s.Close()
	team := r.TeamOf(r.Identity{Tenant: "t", Issuer: "i", Subject: "alice"})
	alice, bob := r.ActorDigest(r.Identity{Tenant: "t", Issuer: "i", Subject: "alice"}), r.ActorDigest(r.Identity{Tenant: "t", Issuer: "i", Subject: "bob"})
	e1, err := s.SaveAs(team, alice, "", 0, "v1")
	if err != nil || e1.SavedBy != alice {
		t.Fatal(e1, err)
	}
	if _, err := s.PublishAs(team, alice, e1.ID, "publish", 1, 0); !errors.Is(err, privatedocs.ErrApprovalRequired) {
		t.Fatalf("the saver approved its own version: %v", err)
	}
	if st, err := s.PublishAs(team, bob, e1.ID, "publish", 1, 0); err != nil || st.PublishedVersion != 1 {
		t.Fatal(st, err)
	}
	if st, err := s.PublishAs(team, alice, e1.ID, "withdraw", 0, 1); err != nil || st.PublishedVersion != 0 {
		t.Fatal("a withdrawal by the saver", st, err)
	}
	// A version saved without a saver cannot be approved in a shared space.
	e2, _ := s.Save(team, "", 0, "unsigned")
	if _, err := s.PublishAs(team, bob, e2.ID, "publish", 1, 0); !errors.Is(err, privatedocs.ErrApprovalRequired) {
		t.Fatalf("an unsigned version: %v", err)
	}
	// One's own designs: no approver, no rule.
	mine := r.Identity{Tenant: "t", Issuer: "i", Subject: "alice"}
	e3, _ := s.Save(mine, "", 0, "mine")
	if _, err := s.Publish(mine, e3.ID, "publish", 1, 0); err != nil {
		t.Fatal(err)
	}
}
