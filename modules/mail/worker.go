package mail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	netmail "net/mail"
	"path/filepath"
	"strings"

	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type AttachmentOpener interface {
	Open(context.Context, security.Actor, file.ID) (file.OpenedFile, error)
}

type Worker struct {
	siteID      site.ID
	repository  Repository
	files       AttachmentOpener
	spool       *AttachmentSpool
	transport   Transport
	lifecycle   *runtimeLifecycle
	maxAttempts int
	logger      *slog.Logger
}

func NewWorker(siteID site.ID, repository Repository, files AttachmentOpener, spool *AttachmentSpool, transport Transport, maxAttempts int, logger *slog.Logger) (*Worker, error) {
	return newWorker(siteID, repository, files, spool, transport, &runtimeLifecycle{}, maxAttempts, logger)
}

func newWorker(siteID site.ID, repository Repository, files AttachmentOpener, spool *AttachmentSpool, transport Transport, lifecycle *runtimeLifecycle, maxAttempts int, logger *slog.Logger) (*Worker, error) {
	if siteID <= 0 || repository == nil || files == nil || transport == nil || lifecycle == nil || maxAttempts < 1 {
		return nil, errors.New("mail worker dependencies are nil")
	}
	if err := validateTransport(transport); err != nil {
		return nil, err
	}
	return &Worker{siteID: siteID, repository: repository, files: files, spool: spool, transport: transport, lifecycle: lifecycle, maxAttempts: maxAttempts, logger: logger}, nil
}

func (w *Worker) Handle(ctx context.Context, item job.Envelope) error {
	if item.Name != SendJobName || item.SchemaVersion != 1 {
		return errors.New("mail send job envelope is invalid")
	}
	if item.ScopeID != fmt.Sprint(w.siteID) {
		return errors.New("mail send job site scope is invalid")
	}
	var payload struct {
		MessageID MessageID `json:"message_id"`
	}
	if err := json.Unmarshal(item.Payload, &payload); err != nil || payload.MessageID <= 0 {
		return errors.New("mail send job payload is invalid")
	}
	var message Message
	var attempt DeliveryAttempt
	var claimed bool
	err := w.lifecycle.withActive(func() error {
		var claimErr error
		message, attempt, claimed, claimErr = w.repository.ClaimMessage(ctx, w.siteID, payload.MessageID, w.maxAttempts)
		return claimErr
	})
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil || !claimed {
		return err
	}
	w.logAttempt(ctx, message, attempt, "mail.attempt.started", nil)
	if err := validateImmutableMessage(message); err != nil {
		return w.finishTerminal(ctx, message, attempt.AttemptNumber, DeliveryResult{Driver: w.transport.Driver()}, terminalDeliveryError("message_invalid", err))
	}
	attachments := make([]DeliveryAttachment, 0, len(message.Attachments))
	closers := make([]io.Closer, 0, len(message.Attachments))
	defer func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}()
	for _, attachment := range message.Attachments {
		if attachment.Source == AttachmentTransient {
			if w.spool == nil {
				err = errors.New("mail transient attachment spool is unavailable")
				closeAll(closers)
				return w.finishTerminal(ctx, message, attempt.AttemptNumber, DeliveryResult{Driver: w.transport.Driver()}, terminalDeliveryError("attachment_missing", err))
			}
			body, openErr := w.spool.Open(ctx, attachment)
			if openErr != nil {
				err = fmt.Errorf("open transient mail attachment: %w", openErr)
				closeAll(closers)
				return w.finishTerminal(ctx, message, attempt.AttemptNumber, DeliveryResult{Driver: w.transport.Driver()}, terminalDeliveryError("attachment_missing", err))
			}
			closers = append(closers, body)
			attachments = append(attachments, DeliveryAttachment{Attachment: attachment, Body: body})
			continue
		}
		if attachment.FileID == nil {
			err = errors.New("mail attachment has no persistent file reference")
			closeAll(closers)
			return w.finishTerminal(ctx, message, attempt.AttemptNumber, DeliveryResult{Driver: w.transport.Driver()}, terminalDeliveryError("attachment_missing", err))
		}
		opened, openErr := w.files.Open(ctx, security.System(), *attachment.FileID)
		if openErr != nil {
			err = fmt.Errorf("open mail attachment %d: %w", *attachment.FileID, openErr)
			closeAll(closers)
			return w.finishTerminal(ctx, message, attempt.AttemptNumber, DeliveryResult{Driver: w.transport.Driver()}, terminalDeliveryError("attachment_missing", err))
		}
		closers = append(closers, opened.Body)
		attachments = append(attachments, DeliveryAttachment{Attachment: attachment, Body: opened.Body})
	}
	result, sendErr := w.transport.Send(ctx, Delivery{Message: message, Attachments: attachments})
	closeAll(closers)
	closers = nil
	if result.Driver == "" {
		result.Driver = w.transport.Driver()
	}
	failure := classifyDeliveryError(sendErr)
	terminal := failure != nil && (!failure.Retryable || attempt.AttemptNumber >= w.maxAttempts)
	if finishErr := w.repository.FinishAttempt(ctx, message.ID, attempt.AttemptNumber, result, failure, terminal); finishErr != nil {
		return errors.Join(sendErr, finishErr)
	}
	if failure != nil && !terminal {
		w.logAttempt(ctx, message, attempt, "mail.attempt.failed", failure)
		return failure
	}
	if failure != nil {
		w.logAttempt(ctx, message, attempt, "mail.attempt.failed", failure)
		w.logAttempt(ctx, message, attempt, "mail.message.failed", failure)
	} else {
		w.logAttempt(ctx, message, attempt, "mail.message.accepted", nil)
	}
	w.cleanupTransient(ctx, message)
	return nil
}

func validateImmutableMessage(message Message) error {
	if message.ID <= 0 || message.SiteID <= 0 || message.Status != StatusSending {
		return errors.New("mail immutable message identity or state is invalid")
	}
	if len(message.RFCMessageID) > 998 || !strings.HasPrefix(message.RFCMessageID, "<") || !strings.HasSuffix(message.RFCMessageID, ">") || strings.Count(message.RFCMessageID, "@") != 1 || hasHeaderControl(message.RFCMessageID) {
		return errors.New("mail immutable Message-ID is invalid")
	}
	if hasHeaderControl(message.Subject) || validateImmutableAddress(message.From) != nil {
		return errors.New("mail immutable sender or subject is invalid")
	}
	recipients := append(append(append([]Address(nil), message.To...), message.CC...), message.BCC...)
	if len(recipients) == 0 {
		return ErrNoRecipients
	}
	for _, address := range recipients {
		if err := validateImmutableAddress(address); err != nil {
			return err
		}
	}
	if message.ReplyTo != nil {
		if err := validateImmutableAddress(*message.ReplyTo); err != nil {
			return err
		}
	}
	if message.ContentType != ContentText && message.ContentType != ContentHTML {
		return errors.New("mail immutable content type is invalid")
	}
	for _, attachment := range message.Attachments {
		if attachment.Source != AttachmentStatic && attachment.Source != AttachmentVariable && attachment.Source != AttachmentSite && attachment.Source != AttachmentTransient {
			return errors.New("mail immutable attachment source is invalid")
		}
		if attachment.Filename == "" || filepath.Base(attachment.Filename) != attachment.Filename || hasHeaderControl(attachment.Filename) || attachment.Size < 0 {
			return errors.New("mail immutable attachment metadata is invalid")
		}
		mediaType, _, err := mime.ParseMediaType(attachment.MIMEType)
		if err != nil || mediaType != attachment.MIMEType {
			return errors.New("mail immutable attachment MIME type is invalid")
		}
		if attachment.Source == AttachmentTransient {
			if attachment.FileID != nil || !validSpoolKey(attachment.spoolKey) {
				return errors.New("mail immutable transient attachment reference is invalid")
			}
		} else if attachment.FileID == nil || *attachment.FileID <= 0 {
			return errors.New("mail immutable persistent attachment reference is invalid")
		}
	}
	return nil
}

func validateImmutableAddress(address Address) error {
	if address.Email == "" || hasHeaderControl(address.Name) || hasHeaderControl(address.Email) || strings.ContainsAny(address.Email, "<>") {
		return errors.New("mail immutable address is invalid")
	}
	parsed, err := netmail.ParseAddress(address.Email)
	if err != nil || parsed.Address != address.Email {
		return errors.New("mail immutable address is invalid")
	}
	return nil
}

func hasHeaderControl(value string) bool {
	return strings.IndexFunc(value, func(current rune) bool {
		return current == '\x7f' || current < ' '
	}) >= 0
}

func closeAll(closers []io.Closer) {
	for _, closer := range closers {
		_ = closer.Close()
	}
}

func (w *Worker) logAttempt(ctx context.Context, message Message, attempt DeliveryAttempt, event string, failure *DeliveryError) {
	if w.logger == nil {
		return
	}
	attributes := []any{slog.String("event", event), slog.Int64("message_id", int64(message.ID)), slog.Int64("site_id", int64(message.SiteID)), slog.Int("attempt", attempt.AttemptNumber), slog.String("driver", w.transport.Driver())}
	if failure != nil {
		attributes = append(attributes, slog.String("error_code", failure.Code), slog.Bool("retryable", failure.Retryable))
	}
	w.logger.Log(ctx, slog.LevelInfo, "mail delivery attempt completed", attributes...)
}

func (w *Worker) finish(ctx context.Context, id MessageID, attempt int, result DeliveryResult, failure *DeliveryError, terminal bool) error {
	if err := w.repository.FinishAttempt(ctx, id, attempt, result, failure, terminal); err != nil {
		return errors.Join(failure, err)
	}
	if failure != nil && !terminal {
		return failure
	}
	return nil
}

func (w *Worker) finishTerminal(ctx context.Context, message Message, attempt int, result DeliveryResult, failure *DeliveryError) error {
	if err := w.finish(ctx, message.ID, attempt, result, failure, true); err != nil {
		return err
	}
	w.logAttempt(ctx, message, DeliveryAttempt{AttemptNumber: attempt}, "mail.attempt.failed", failure)
	w.logAttempt(ctx, message, DeliveryAttempt{AttemptNumber: attempt}, "mail.message.failed", failure)
	w.cleanupTransient(ctx, message)
	return nil
}

func (w *Worker) cleanupTransient(ctx context.Context, message Message) {
	if w.spool == nil {
		return
	}
	for _, attachment := range message.Attachments {
		if attachment.Source != AttachmentTransient {
			continue
		}
		if err := w.spool.Delete(context.WithoutCancel(ctx), attachment); err != nil && w.logger != nil {
			w.logger.ErrorContext(context.WithoutCancel(ctx), "mail spool terminal cleanup failed", slog.String("event", "mail.spool.cleanup.failed"), slog.Int64("message_id", int64(message.ID)), slog.Any("error", err))
		}
	}
}
