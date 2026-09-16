package resource_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestResourceEventUsesExplicitPayload(t *testing.T) {
	actor := security.UserID(9)
	event, err := resource.NewEvent(resource.EventUpdated, time.Now(), resource.EventPayload{
		ResourceID: 42, SiteID: site.ID(7), StorageKind: resource.StorageLibraryItem, Version: 19, ActorID: &actor,
	})
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"resource_id": float64(42), "site_id": float64(7), "storage_kind": "library_item", "version": float64(19), "actor_id": float64(9), "operation": ""}
	if len(payload) != len(want) {
		t.Fatalf("payload contains unexpected resource snapshot fields: %#v", payload)
	}
	for key, value := range want {
		if payload[key] != value {
			t.Fatalf("payload[%q] = %#v, want %#v", key, payload[key], value)
		}
	}
}

func TestResourceEventRejectsUnsupportedFacts(t *testing.T) {
	_, err := resource.NewEvent("resource.before_create", time.Now(), resource.EventPayload{ResourceID: 1, SiteID: 1, StorageKind: resource.StorageTree, Version: 1})
	if err == nil {
		t.Fatal("unsupported resource event was accepted")
	}
}
