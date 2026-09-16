package entityhooks_test

import (
	"context"
	"errors"
	"testing"

	"github.com/vernal96/go-cms-kernel/entityhooks"
)

type draft struct{ Value string }

func TestTypedRegistrationOrderAndVeto(t *testing.T) {
	r := entityhooks.NewRegistry(entityhooks.Site, "7")
	key := entityhooks.NewKey[draft]("catalog", "catalog.before_create", entityhooks.Site)
	first := r.ForModule("catalog", nil)
	second := r.ForModule("custom", []string{"catalog"})
	if err := entityhooks.RegisterBefore(first, key, "defaults", func(_ context.Context, d *draft) error { d.Value += "A"; return nil }); err != nil {
		t.Fatal(err)
	}
	veto := errors.New("veto")
	if err := entityhooks.RegisterBefore(second, key, "policy", func(_ context.Context, d *draft) error {
		if d.Value != "A" {
			t.Fatalf("previous hook not visible: %q", d.Value)
		}
		d.Value += "B"
		return veto
	}); err != nil {
		t.Fatal(err)
	}
	if err := entityhooks.RegisterBefore(second, key, "last", func(context.Context, *draft) error { t.Fatal("ran after veto"); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := r.Seal(); err != nil {
		t.Fatal(err)
	}
	d := draft{}
	if err := entityhooks.Before(context.Background(), r, key, &d); !errors.Is(err, veto) {
		t.Fatalf("error=%v", err)
	}
	if d.Value != "AB" {
		t.Fatalf("value=%q", d.Value)
	}
}

func TestRegistryRejectsInvalidContributions(t *testing.T) {
	key := entityhooks.NewKey[draft]("catalog", "catalog.before_create", entityhooks.Site)
	handler := func(context.Context, *draft) error { return nil }
	r := entityhooks.NewRegistry(entityhooks.Site, "1")
	if err := entityhooks.RegisterBefore(r.ForModule("other", nil), key, "x", handler); err == nil {
		t.Fatal("undeclared dependency accepted")
	}
	if err := entityhooks.RegisterBefore(entityhooks.NewRegistry(entityhooks.Application, "").ForModule("catalog", nil), key, "x", handler); err == nil {
		t.Fatal("wrong scope accepted")
	}
	registrar := r.ForModule("catalog", nil)
	if err := entityhooks.RegisterBefore(registrar, key, "x", handler); err != nil {
		t.Fatal(err)
	}
	if err := entityhooks.RegisterBefore(registrar, key, "x", handler); err == nil {
		t.Fatal("duplicate accepted")
	}
	other := entityhooks.NewKey[string]("catalog", key.Name(), entityhooks.Site)
	if err := entityhooks.RegisterBefore(registrar, other, "y", func(context.Context, *string) error { return nil }); err == nil {
		t.Fatal("conflicting payload accepted")
	}
	if err := r.Seal(); err != nil {
		t.Fatal(err)
	}
	if err := entityhooks.RegisterBefore(registrar, key, "z", handler); !errors.Is(err, entityhooks.ErrSealed) {
		t.Fatalf("late registration: %v", err)
	}
}

func TestRegistryDrainExcludesWritesAndAbortRestores(t *testing.T) {
	r := entityhooks.EmptyRegistry(entityhooks.Site, "1")
	release, err := r.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Drain(); !errors.Is(err, entityhooks.ErrBusy) {
		t.Fatalf("active mutation drain=%v", err)
	}
	release()
	release()
	abort, err := r.Drain()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Acquire(); !errors.Is(err, entityhooks.ErrBusy) {
		t.Fatalf("draining acquire=%v", err)
	}
	abort()
	abort()
	release, err = r.Acquire()
	if err != nil {
		t.Fatal(err)
	}
	release()
}
