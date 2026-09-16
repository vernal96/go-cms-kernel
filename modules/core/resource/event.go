package resource

import (
	"errors"
	"time"

	"github.com/vernal96/go-cms-kernel/domainevent"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

const (
	EventDeleted       = "resource.deleted"
	EventCreated       = "resource.created"
	EventUpdated       = "resource.updated"
	EventSchemaVersion = 1
)

type EventPayload struct {
	Operation   Operation        `json:"operation"`
	Before      *EventState      `json:"before,omitempty"`
	After       *EventState      `json:"after,omitempty"`
	ResourceID  ID               `json:"resource_id"`
	SiteID      site.ID          `json:"site_id"`
	StorageKind StorageKind      `json:"storage_kind"`
	Version     int64            `json:"version"`
	ActorID     *security.UserID `json:"actor_id,omitempty"`
}

func NewEvent(name string, occurredAt time.Time, payload EventPayload) (domainevent.Envelope, error) {
	if name != EventCreated && name != EventUpdated && name != EventDeleted {
		return domainevent.Envelope{}, errors.New("resource event name is unsupported")
	}
	if payload.ResourceID <= 0 || payload.SiteID <= 0 || payload.Version <= 0 ||
		(payload.StorageKind != StorageTree && payload.StorageKind != StorageLibraryItem) {
		return domainevent.Envelope{}, ErrInvalid
	}
	return domainevent.New(name, EventSchemaVersion, occurredAt, payload)
}
