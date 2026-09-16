package messageid_test

import (
	"testing"

	"github.com/vernal96/go-cms-kernel/messageid"
)

func TestNewProducesDistinctValidIDs(t *testing.T) {
	first, err := messageid.New()
	if err != nil {
		t.Fatal(err)
	}
	second, err := messageid.New()
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("duplicate message ID %q", first)
	}
	if err := first.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := messageid.ID("not-an-id").Validate(); err == nil {
		t.Fatal("invalid message ID was accepted")
	}
}
