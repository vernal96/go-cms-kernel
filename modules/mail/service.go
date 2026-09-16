package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/messageid"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	coreuser "github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

var (
	TemplateReadPermission   = permission.MustCode("mail", "template", permission.Read)
	TemplateCreatePermission = permission.MustCode("mail", "template", permission.Create)
	TemplateUpdatePermission = permission.MustCode("mail", "template", permission.Update)
	TemplateDeletePermission = permission.MustCode("mail", "template", permission.Delete)
	MessageReadPermission    = permission.MustCode("mail", "message", permission.Read)
	MessageCreatePermission  = permission.MustCode("mail", "message", permission.Create)
	MessageDeletePermission  = permission.MustCode("mail", "message", permission.Delete)
)

const SendJobName = "mail.send"

var messageIDDomainPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]{0,253}[A-Za-z0-9])?$`)

type Service struct {
	siteID     site.ID
	repository Repository
	renderer   *Renderer
	lifecycle  *runtimeLifecycle
	authorizer security.Authorizer
	users      interface {
		Current(context.Context, security.Actor) (coreuser.User, error)
	}
	spool           *AttachmentSpool
	limits          Limits
	messageIDDomain string
	uploadStorage   filesystem.Code
	uploadPath      string
	logger          *slog.Logger
}

type EditorConfig struct {
	UploadStorage filesystem.Code
	UploadPath    string
}

func (s *Service) EditorConfig() EditorConfig {
	return EditorConfig{UploadStorage: s.uploadStorage, UploadPath: s.uploadPath}
}

func NewService(siteID site.ID, repository Repository, renderer *Renderer, authorizer security.Authorizer, users interface {
	Current(context.Context, security.Actor) (coreuser.User, error)
}, spool *AttachmentSpool, limits Limits, messageIDDomain string) (*Service, error) {
	if siteID <= 0 {
		return nil, errors.New("mail service site ID is invalid")
	}
	if repository == nil || renderer == nil || authorizer == nil || users == nil {
		return nil, errors.New("mail service dependencies are nil")
	}
	if limits.MaxRecipients < 1 || limits.MaxMessageSize < 1 || limits.MaxAttachmentSize < 1 {
		return nil, errors.New("mail service limits are invalid")
	}
	messageIDDomain = strings.TrimSpace(messageIDDomain)
	if messageIDDomain == "" {
		messageIDDomain = "localhost"
	}
	if !messageIDDomainPattern.MatchString(messageIDDomain) {
		return nil, errors.New("mail Message-ID domain is invalid")
	}
	return &Service{siteID: siteID, repository: repository, renderer: renderer, lifecycle: &runtimeLifecycle{}, authorizer: authorizer, users: users, spool: spool, limits: limits, messageIDDomain: messageIDDomain}, nil
}

func (s *Service) ListTemplates(ctx context.Context, actor security.Actor, query PageQuery) (TemplatePage, error) {
	if err := s.authorizer.Check(ctx, actor, TemplateReadPermission); err != nil {
		return TemplatePage{}, err
	}
	query, err := normalizePage(query)
	if err != nil {
		return TemplatePage{}, err
	}
	return s.repository.ListTemplates(ctx, s.siteID, query)
}

func (s *Service) Template(ctx context.Context, actor security.Actor, id TemplateID) (Template, error) {
	if err := s.authorizer.Check(ctx, actor, TemplateReadPermission); err != nil {
		return Template{}, err
	}
	return s.repository.TemplateByID(ctx, s.siteID, id)
}

// IntegrationTemplate returns safe metadata for another same-site module's
// admin configuration flow. The initiating actor must be allowed to read Mail
// templates; automatic execution later uses the already-trusted template Code.
func (s *Service) IntegrationTemplate(ctx context.Context, actor security.Actor, code string) (IntegrationTemplateMetadata, error) {
	if err := s.authorizer.Check(ctx, actor, TemplateReadPermission); err != nil {
		return IntegrationTemplateMetadata{}, err
	}
	template, err := s.repository.TemplateByCode(ctx, s.siteID, strings.TrimSpace(code))
	if err != nil {
		return IntegrationTemplateMetadata{}, err
	}
	return IntegrationTemplateMetadata{
		Code: template.Code, Name: template.Name, Enabled: template.Enabled,
		Variables: field.CloneDefinitions(template.Variables),
	}, nil
}

func (s *Service) SendTemplates(ctx context.Context, actor security.Actor, query PageQuery) (TemplatePage, error) {
	if err := s.authorizer.Check(ctx, actor, MessageCreatePermission); err != nil {
		return TemplatePage{}, err
	}
	query, err := normalizePage(query)
	if err != nil {
		return TemplatePage{}, err
	}
	return s.repository.ListEnabledTemplates(ctx, s.siteID, query)
}

func (s *Service) CreateTemplate(ctx context.Context, actor security.Actor, template Template) (Template, error) {
	if err := s.authorizer.Check(ctx, actor, TemplateCreatePermission); err != nil {
		return Template{}, err
	}
	template.ID, template.SiteID = 0, s.siteID
	if err := s.validateTemplateRuntime(template); err != nil {
		return Template{}, err
	}
	if err := s.renderer.ValidateTemplateFiles(ctx, actor, template); err != nil {
		return Template{}, err
	}
	template.CreatedBy, template.UpdatedBy = actor.AuditUserID(), actor.AuditUserID()
	return s.repository.CreateTemplate(ctx, template)
}

func (s *Service) UpdateTemplate(ctx context.Context, actor security.Actor, template Template) (Template, error) {
	if err := s.authorizer.Check(ctx, actor, TemplateUpdatePermission); err != nil {
		return Template{}, err
	}
	if template.ID <= 0 {
		return Template{}, fmt.Errorf("%w: template ID is invalid", ErrInvalid)
	}
	template.SiteID = s.siteID
	if err := s.validateTemplateRuntime(template); err != nil {
		return Template{}, err
	}
	if err := s.renderer.ValidateTemplateFiles(ctx, actor, template); err != nil {
		return Template{}, err
	}
	template.UpdatedBy = actor.AuditUserID()
	return s.repository.UpdateTemplate(ctx, template)
}

func (s *Service) DeleteTemplate(ctx context.Context, actor security.Actor, id TemplateID) error {
	if err := s.authorizer.Check(ctx, actor, TemplateDeletePermission); err != nil {
		return err
	}
	return s.repository.DeleteTemplate(ctx, s.siteID, id)
}

func (s *Service) SetTemplateEnabled(ctx context.Context, actor security.Actor, id TemplateID, enabled bool) (Template, error) {
	if err := s.authorizer.Check(ctx, actor, TemplateUpdatePermission); err != nil {
		return Template{}, err
	}
	if id <= 0 {
		return Template{}, fmt.Errorf("%w: template ID is invalid", ErrInvalid)
	}
	if enabled {
		template, err := s.repository.TemplateByID(ctx, s.siteID, id)
		if err != nil {
			return Template{}, err
		}
		if err := s.validateTemplateRuntime(template); err != nil {
			return Template{}, err
		}
	}
	return s.repository.SetTemplateEnabled(ctx, s.siteID, id, enabled, actor.AuditUserID())
}

func (s *Service) Preview(ctx context.Context, actor security.Actor, templateID TemplateID, values map[string]any) (RenderedMessage, error) {
	if err := s.authorizer.Check(ctx, actor, MessageCreatePermission); err != nil {
		return RenderedMessage{}, err
	}
	template, err := s.repository.TemplateByID(ctx, s.siteID, templateID)
	if err != nil {
		return RenderedMessage{}, err
	}
	if !template.Enabled {
		return RenderedMessage{}, ErrTemplateDisabled
	}
	if err := s.validateTemplateRuntime(template); err != nil {
		return RenderedMessage{}, err
	}
	rendered, err := s.renderer.Render(ctx, template, values, actor)
	if err != nil {
		return RenderedMessage{}, err
	}
	if len(rendered.To)+len(rendered.CC)+len(rendered.BCC) > s.limits.MaxRecipients {
		return RenderedMessage{}, fmt.Errorf("%w: recipient limit exceeded", ErrInvalid)
	}
	if err := s.validateMessageSize(rendered); err != nil {
		return RenderedMessage{}, err
	}
	return rendered, nil
}

func (s *Service) QueueManual(ctx context.Context, actor security.Actor, input ManualSendInput) (Message, error) {
	if err := s.authorizer.Check(ctx, actor, MessageCreatePermission); err != nil {
		return Message{}, err
	}
	template, err := s.repository.TemplateByID(ctx, s.siteID, input.TemplateID)
	if err != nil {
		return Message{}, err
	}
	requester, err := s.users.Current(ctx, actor)
	if err != nil {
		return Message{}, err
	}
	return s.queue(ctx, template, input.Values, nil, actor, Origin{Kind: OriginManual, RequestedBy: actor.AuditUserID(), RequestedName: userDisplayName(requester)})
}

func userDisplayName(item coreuser.User) string {
	parts := make([]string, 0, 3)
	if value := strings.TrimSpace(item.Name); value != "" {
		parts = append(parts, value)
	}
	if item.MiddleName != nil {
		if value := strings.TrimSpace(*item.MiddleName); value != "" {
			parts = append(parts, value)
		}
	}
	if item.LastName != nil {
		if value := strings.TrimSpace(*item.LastName); value != "" {
			parts = append(parts, value)
		}
	}
	result := strings.Join(parts, " ")
	if result == "" {
		result = strings.TrimSpace(item.Login)
	}
	return result
}

// QueueByCode is the stable programmatic integration surface for other modules.
// Infrastructure, database IDs and broker details remain internal to Mail.
func (s *Service) QueueByCode(ctx context.Context, input QueueInput) (Message, error) {
	template, err := s.repository.TemplateByCode(ctx, s.siteID, input.TemplateCode)
	if err != nil {
		return Message{}, err
	}
	input.Origin.Kind = OriginAutomatic
	input.Origin.RequestedBy = nil
	input.Origin.RequestedName = ""
	return s.queue(ctx, template, input.Values, input.Attachments, security.System(), input.Origin)
}

func (s *Service) queue(ctx context.Context, template Template, values map[string]any, transient []TransientAttachment, renderActor security.Actor, origin Origin) (_ Message, resultErr error) {
	var result Message
	err := s.lifecycle.withActive(func() error {
		var err error
		result, err = s.queueActive(ctx, template, values, transient, renderActor, origin)
		return err
	})
	return result, err
}

func (s *Service) queueActive(ctx context.Context, template Template, values map[string]any, transient []TransientAttachment, renderActor security.Actor, origin Origin) (_ Message, resultErr error) {
	if !template.Enabled {
		return Message{}, ErrTemplateDisabled
	}
	if err := s.validateTemplateRuntime(template); err != nil {
		return Message{}, err
	}
	rendered, err := s.renderer.Render(ctx, template, values, renderActor)
	if err != nil {
		return Message{}, err
	}
	if len(rendered.To)+len(rendered.CC)+len(rendered.BCC) > s.limits.MaxRecipients {
		return Message{}, fmt.Errorf("%w: recipient limit exceeded", ErrInvalid)
	}
	preflight := rendered
	preflight.Attachments = append([]Attachment(nil), rendered.Attachments...)
	for _, input := range transient {
		if s.spool == nil {
			return Message{}, errors.New("mail transient attachments are disabled")
		}
		preflight.Attachments = append(preflight.Attachments, Attachment{
			Source: AttachmentTransient, Filename: strings.TrimSpace(input.Filename),
			MIMEType: strings.TrimSpace(input.MIMEType), Size: input.Size,
		})
	}
	if err := s.validateMessageSize(preflight); err != nil {
		return Message{}, err
	}
	spooled := make([]Attachment, 0, len(transient))
	defer func() {
		if resultErr == nil {
			return
		}
		for _, attachment := range spooled {
			if cleanupErr := s.spool.Delete(context.WithoutCancel(ctx), attachment); cleanupErr != nil && s.logger != nil {
				s.logger.ErrorContext(context.WithoutCancel(ctx), "mail spool queue rollback cleanup failed", slog.String("event", "mail.spool.cleanup.failed"), slog.Int64("site_id", int64(s.siteID)), slog.String("reason", "queue_rollback"), slog.Any("error", cleanupErr))
			}
		}
	}()
	for _, input := range transient {
		attachment, putErr := s.spool.Put(ctx, input, s.limits.MaxAttachmentSize)
		if putErr != nil {
			return Message{}, putErr
		}
		spooled = append(spooled, attachment)
	}
	rendered.Attachments = append(rendered.Attachments, spooled...)
	if err := s.validateMessageSize(rendered); err != nil {
		return Message{}, err
	}
	id, err := messageid.New()
	if err != nil {
		return Message{}, fmt.Errorf("create mail message identity: %w", err)
	}
	templateID := template.ID
	now := time.Now().UTC()
	message := Message{
		SiteID: s.siteID, TemplateID: &templateID, TemplateCode: template.Code, TemplateName: template.Name,
		RFCMessageID: fmt.Sprintf("<%s@%s>", id, s.messageIDDomain),
		From:         rendered.From, To: rendered.To, CC: rendered.CC, BCC: rendered.BCC, ReplyTo: rendered.ReplyTo,
		Subject: rendered.Subject, ContentType: rendered.ContentType, TextBody: rendered.TextBody, HTMLBody: rendered.HTMLBody,
		Attachments: rendered.Attachments, Status: StatusQueued, Origin: origin.Kind, OriginSource: strings.TrimSpace(origin.Source), OriginEvent: strings.TrimSpace(origin.Event), OriginReference: strings.TrimSpace(origin.Reference),
		RequestedAt: now, RequestedBy: origin.RequestedBy, RequestedByName: strings.TrimSpace(origin.RequestedName),
	}
	item, err := job.NewScoped(SendJobName, 1, fmt.Sprint(s.siteID), struct {
		MessageID MessageID `json:"message_id"`
	}{MessageID: 0})
	if err != nil {
		return Message{}, err
	}
	// The adapter allocates the message ID and replaces the zero payload ID in
	// the same transaction before inserting the outbox row.
	body, err := json.Marshal(item)
	if err != nil {
		return Message{}, err
	}
	queued, err := s.repository.CreateMessageAndJob(ctx, message, eventbus.Message{
		Topic: job.Topic(SendJobName), Key: []byte(item.ID), Body: body,
		Headers: map[string][]byte{"content-type": []byte("application/json"), "x-cms-message-id": []byte(item.ID), "x-cms-job-name": []byte(SendJobName)},
	})
	if err != nil {
		return Message{}, err
	}
	if s.logger != nil {
		s.logger.InfoContext(ctx, "mail message queued", slog.String("event", "mail.message.queued"), slog.Int64("message_id", int64(queued.ID)), slog.Int64("site_id", int64(queued.SiteID)), slog.String("template_code", queued.TemplateCode), slog.String("origin", string(queued.Origin)), slog.Int("recipient_count", len(queued.To)+len(queued.CC)+len(queued.BCC)), slog.Int("attachment_count", len(queued.Attachments)))
	}
	return queued, nil
}

func (s *Service) validateTemplateRuntime(template Template) error {
	return s.renderer.ValidateTemplate(template)
}

func (s *Service) validateMessageSize(rendered RenderedMessage) error {
	bodySize := int64(len(rendered.TextBody) + len(rendered.HTMLBody))
	size := int64(len(rendered.Subject)+len(rendered.From.Name)+len(rendered.From.Email)) + 2048
	if size > s.limits.MaxMessageSize || !addEstimatedSize(&size, base64WireSize(bodySize), s.limits.MaxMessageSize) {
		return fmt.Errorf("%w: message size limit exceeded", ErrInvalid)
	}
	for _, address := range append(append(append([]Address(nil), rendered.To...), rendered.CC...), rendered.BCC...) {
		if !addEstimatedSize(&size, int64(len(address.Name)+len(address.Email)), s.limits.MaxMessageSize) {
			return fmt.Errorf("%w: message size limit exceeded", ErrInvalid)
		}
	}
	for _, attachment := range rendered.Attachments {
		if attachment.Size < 0 || attachment.Size > s.limits.MaxAttachmentSize {
			return fmt.Errorf("%w: attachment size limit exceeded", ErrInvalid)
		}
		headerSize := int64(len(attachment.Filename) + len(attachment.MIMEType) + 256)
		if !addEstimatedSize(&size, headerSize, s.limits.MaxMessageSize) || !addEstimatedSize(&size, base64WireSize(attachment.Size), s.limits.MaxMessageSize) {
			return fmt.Errorf("%w: message size limit exceeded", ErrInvalid)
		}
	}
	return nil
}

func addEstimatedSize(total *int64, increment, limit int64) bool {
	if increment < 0 || *total < 0 || *total > limit || increment > limit-*total {
		return false
	}
	*total += increment
	return true
}

func base64WireSize(size int64) int64 {
	if size <= 0 {
		return 2
	}
	const maximum = int64(^uint64(0) >> 1)
	if size > maximum-2 {
		return maximum
	}
	units := (size + 2) / 3
	if units > maximum/4 {
		return maximum
	}
	encoded := 4 * units
	if encoded > maximum-75 {
		return maximum
	}
	lineBreaks := 2 * ((encoded + 75) / 76)
	if lineBreaks < 0 || lineBreaks > maximum-encoded {
		return maximum
	}
	return encoded + lineBreaks
}

func (s *Service) ListMessages(ctx context.Context, actor security.Actor, query MessageQuery) (MessageSummaryPage, error) {
	if err := s.authorizer.Check(ctx, actor, MessageReadPermission); err != nil {
		return MessageSummaryPage{}, err
	}
	page, err := normalizePage(query.PageQuery)
	if err != nil {
		return MessageSummaryPage{}, err
	}
	query.PageQuery = page
	query.TemplateCode = strings.TrimSpace(query.TemplateCode)
	query.Recipient = strings.TrimSpace(query.Recipient)
	if query.Status != "" && query.Status != StatusQueued && query.Status != StatusSending && query.Status != StatusRetryable && query.Status != StatusAccepted && query.Status != StatusFailed {
		return MessageSummaryPage{}, fmt.Errorf("%w: message status filter is invalid", ErrInvalid)
	}
	if len(query.TemplateCode) > 64 || len(query.Recipient) > 320 || (query.DateFrom != nil && query.DateTo != nil && query.DateFrom.After(*query.DateTo)) {
		return MessageSummaryPage{}, fmt.Errorf("%w: message filters are invalid", ErrInvalid)
	}
	return s.repository.ListMessages(ctx, s.siteID, query)
}

func (s *Service) SiteVariables(ctx context.Context, actor security.Actor) ([]site.TemplateVariable, error) {
	if readErr := s.authorizer.Check(ctx, actor, TemplateReadPermission); readErr != nil {
		if createErr := s.authorizer.Check(ctx, actor, TemplateCreatePermission); createErr != nil {
			return nil, readErr
		}
	}
	return s.renderer.SiteVariables(), nil
}

func (s *Service) Message(ctx context.Context, actor security.Actor, id MessageID) (MessageDetail, error) {
	if err := s.authorizer.Check(ctx, actor, MessageReadPermission); err != nil {
		return MessageDetail{}, err
	}
	return s.repository.MessageDetail(ctx, s.siteID, id)
}

func (s *Service) DeleteMessage(ctx context.Context, actor security.Actor, id MessageID) error {
	if err := s.authorizer.Check(ctx, actor, MessageDeletePermission); err != nil {
		return err
	}
	return s.repository.DeleteMessage(ctx, s.siteID, id)
}

func normalizePage(query PageQuery) (PageQuery, error) {
	if query.Page == 0 {
		query.Page = 1
	}
	if query.PerPage == 0 {
		query.PerPage = 20
	}
	if query.Page < 1 || query.PerPage < 1 || query.PerPage > 100 {
		return PageQuery{}, fmt.Errorf("%w: pagination is invalid", ErrInvalid)
	}
	return query, nil
}
