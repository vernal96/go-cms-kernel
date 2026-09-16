package mail

import (
	"context"
	"time"

	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type Repository interface {
	ListTemplates(context.Context, site.ID, PageQuery) (TemplatePage, error)
	ListEnabledTemplates(context.Context, site.ID, PageQuery) (TemplatePage, error)
	TemplateByID(context.Context, site.ID, TemplateID) (Template, error)
	TemplateByCode(context.Context, site.ID, string) (Template, error)
	CreateTemplate(context.Context, Template) (Template, error)
	UpdateTemplate(context.Context, Template) (Template, error)
	SetTemplateEnabled(context.Context, site.ID, TemplateID, bool, *security.UserID) (Template, error)
	DeleteTemplate(context.Context, site.ID, TemplateID) error

	CreateMessageAndJob(context.Context, Message, eventbus.Message) (Message, error)
	ListMessages(context.Context, site.ID, MessageQuery) (MessageSummaryPage, error)
	MessageDetail(context.Context, site.ID, MessageID) (MessageDetail, error)
	DeleteMessage(context.Context, site.ID, MessageID) error
	ClaimMessage(context.Context, site.ID, MessageID, int) (Message, DeliveryAttempt, bool, error)
	FinishAttempt(context.Context, MessageID, int, DeliveryResult, *DeliveryError, bool) error
	HasActiveMessages(context.Context, site.ID) (bool, error)
	Cleanup(context.Context, site.ID, time.Duration, int) (int64, error)
	ActiveSpoolKeys(context.Context, site.ID, []string) (map[string]struct{}, error)
}
