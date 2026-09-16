package security

import "testing"

func TestActorsExposeOnlyUserAuditIdentity(t *testing.T) {
	t.Parallel()

	if !Guest().IsGuest() || Guest().AuditUserID() != nil {
		t.Fatal("invalid guest actor")
	}
	if !System().IsSystem() || System().AuditUserID() != nil {
		t.Fatal("invalid system actor")
	}
	actor := User(42)
	id, exists := actor.UserID()
	if !exists || id != 42 {
		t.Fatalf("user identity = %d, %t", id, exists)
	}
	auditID := actor.AuditUserID()
	if auditID == nil || *auditID != 42 {
		t.Fatalf("audit identity = %#v", auditID)
	}
	*auditID = 7
	id, _ = actor.UserID()
	if id != 42 {
		t.Fatal("actor leaked mutable audit identity")
	}
}
