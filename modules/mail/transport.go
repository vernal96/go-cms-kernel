package mail

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"

	"github.com/vernal96/go-cms-kernel"
)

// Application is Mail's application-scoped infrastructure. The same
// Transport instance is shared by every site runtime in the App.
type Application struct {
	Transport Transport
}

func (Application) ModuleCode() kernel.ModuleCode { return ModuleCode }

type DeliveryAttachment struct {
	Attachment
	Body io.Reader
}

type Delivery struct {
	Message     Message
	Attachments []DeliveryAttachment
}

type Transport interface {
	Driver() string
	Send(context.Context, Delivery) (DeliveryResult, error)
}

type transportValidator interface{ Validate() error }

func validateTransport(transport Transport) error {
	if transport == nil || nilTransport(transport) {
		return fmt.Errorf("mail transport is nil")
	}
	if validator, ok := transport.(transportValidator); ok {
		if err := validator.Validate(); err != nil {
			return fmt.Errorf("mail transport: %w", err)
		}
	}
	return nil
}

func nilTransport(transport Transport) bool {
	value := reflect.ValueOf(transport)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

type InvalidTransport struct{ Name string }

func (t InvalidTransport) Driver() string { return t.Name }
func (t InvalidTransport) Validate() error {
	return fmt.Errorf("unsupported mail transport driver %q", t.Name)
}
func (t InvalidTransport) Send(context.Context, Delivery) (DeliveryResult, error) {
	return DeliveryResult{}, t.Validate()
}

type NullTransport struct{}

func (NullTransport) Driver() string { return "null" }
func (NullTransport) Send(context.Context, Delivery) (DeliveryResult, error) {
	return DeliveryResult{Driver: "null", ResponseCode: "discarded"}, nil
}

type LogTransport struct{ Logger *slog.Logger }

func (LogTransport) Driver() string { return "log" }
func (t LogTransport) Send(ctx context.Context, delivery Delivery) (DeliveryResult, error) {
	logger := t.Logger
	if logger == nil {
		logger = slog.Default()
	}
	logger.InfoContext(ctx, "development mail accepted",
		slog.String("event", "mail.log.accepted"),
		slog.Int64("message_id", int64(delivery.Message.ID)),
		slog.String("rfc_message_id", delivery.Message.RFCMessageID),
		slog.Int("recipient_count", len(delivery.Message.To)+len(delivery.Message.CC)+len(delivery.Message.BCC)),
		slog.Int("attachment_count", len(delivery.Attachments)),
	)
	return DeliveryResult{Driver: "log", ResponseCode: "logged"}, nil
}

var _ kernel.ModuleApplication = Application{}
