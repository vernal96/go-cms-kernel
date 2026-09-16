package mail

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	stdmail "net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"
)

type SMTPConfig struct {
	Host          string
	Port          int
	Username      string
	Password      string
	TLSEnabled    bool
	TLSServerName string
	Timeout       time.Duration
}

type SMTPTransport struct{ config SMTPConfig }

// ConfiguredSMTPTransport keeps declarative project configuration while Mail
// validates it during module build and opens a fresh SMTP session per send.
type ConfiguredSMTPTransport struct{ Config SMTPConfig }

func (t ConfiguredSMTPTransport) Driver() string { return "smtp" }
func (t ConfiguredSMTPTransport) Validate() error {
	_, err := NewSMTPTransport(t.Config)
	return err
}
func (t ConfiguredSMTPTransport) Send(ctx context.Context, delivery Delivery) (DeliveryResult, error) {
	transport, err := NewSMTPTransport(t.Config)
	if err != nil {
		return DeliveryResult{Driver: "smtp"}, err
	}
	return transport.Send(ctx, delivery)
}

func NewSMTPTransport(config SMTPConfig) (*SMTPTransport, error) {
	config.Host = strings.TrimSpace(config.Host)
	config.Username = strings.TrimSpace(config.Username)
	if config.Host == "" || config.Port < 1 || config.Port > 65535 {
		return nil, errors.New("SMTP address is invalid")
	}
	if config.Timeout <= 0 {
		return nil, errors.New("SMTP timeout must be positive")
	}
	if (config.Username == "") != (config.Password == "") {
		return nil, errors.New("SMTP username and password must be configured together")
	}
	if config.TLSEnabled && strings.TrimSpace(config.TLSServerName) == "" {
		config.TLSServerName = config.Host
	}
	return &SMTPTransport{config: config}, nil
}

func (*SMTPTransport) Driver() string { return "smtp" }

func (t *SMTPTransport) Send(ctx context.Context, delivery Delivery) (DeliveryResult, error) {
	if ctx == nil {
		return DeliveryResult{}, errors.New("SMTP send context is nil")
	}
	address := net.JoinHostPort(t.config.Host, strconv.Itoa(t.config.Port))
	dialer := net.Dialer{Timeout: t.config.Timeout}
	connection, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return DeliveryResult{Driver: "smtp"}, fmt.Errorf("connect SMTP: %w", err)
	}
	defer connection.Close()
	deadline := time.Now().Add(t.config.Timeout)
	if contextDeadline, exists := ctx.Deadline(); exists && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return DeliveryResult{Driver: "smtp"}, fmt.Errorf("set SMTP deadline: %w", err)
	}
	client, err := smtp.NewClient(connection, t.config.Host)
	if err != nil {
		return DeliveryResult{Driver: "smtp"}, fmt.Errorf("start SMTP client: %w", err)
	}
	defer client.Close()
	if t.config.TLSEnabled {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return DeliveryResult{Driver: "smtp"}, errors.New("SMTP server does not offer required STARTTLS")
		}
		if err := client.StartTLS(&tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.config.TLSServerName}); err != nil {
			return DeliveryResult{Driver: "smtp"}, fmt.Errorf("start SMTP TLS: %w", err)
		}
	}
	if t.config.Username != "" {
		if err := client.Auth(smtp.PlainAuth("", t.config.Username, t.config.Password, t.config.Host)); err != nil {
			return DeliveryResult{Driver: "smtp"}, fmt.Errorf("authenticate SMTP: %w", err)
		}
	}
	if err := client.Mail(delivery.Message.From.Email); err != nil {
		return DeliveryResult{Driver: "smtp"}, fmt.Errorf("set SMTP sender: %w", err)
	}
	for _, recipient := range envelopeRecipients(delivery.Message) {
		if err := client.Rcpt(recipient); err != nil {
			return DeliveryResult{Driver: "smtp"}, fmt.Errorf("set SMTP recipient: %w", err)
		}
	}
	data, err := client.Data()
	if err != nil {
		return DeliveryResult{Driver: "smtp"}, fmt.Errorf("open SMTP data: %w", err)
	}
	writeErr := writeMIMEMessage(data, delivery)
	closeErr := data.Close()
	if writeErr != nil || closeErr != nil {
		return DeliveryResult{Driver: "smtp"}, errors.Join(writeErr, closeErr)
	}
	if err := client.Quit(); err != nil {
		return DeliveryResult{Driver: "smtp"}, fmt.Errorf("finish SMTP session: %w", err)
	}
	return DeliveryResult{Driver: "smtp", ResponseCode: "250"}, nil
}

func envelopeRecipients(message Message) []string {
	result := make([]string, 0, len(message.To)+len(message.CC)+len(message.BCC))
	for _, collection := range [][]Address{message.To, message.CC, message.BCC} {
		for _, address := range collection {
			result = append(result, address.Email)
		}
	}
	return result
}

func writeMIMEMessage(target io.Writer, delivery Delivery) error {
	buffer := bufio.NewWriter(target)
	message := delivery.Message
	headers := []struct{ key, value string }{
		{"Message-ID", message.RFCMessageID},
		{"Date", message.RequestedAt.Format(time.RFC1123Z)},
		{"From", formatAddress(message.From)},
		{"To", formatAddresses(message.To)},
		{"Subject", mime.QEncoding.Encode("UTF-8", message.Subject)},
		{"MIME-Version", "1.0"},
	}
	if len(message.CC) > 0 {
		headers = append(headers, struct{ key, value string }{"Cc", formatAddresses(message.CC)})
	}
	if message.ReplyTo != nil {
		headers = append(headers, struct{ key, value string }{"Reply-To", formatAddress(*message.ReplyTo)})
	}
	for _, header := range headers {
		if header.value == "" {
			continue
		}
		if _, err := fmt.Fprintf(buffer, "%s: %s\r\n", header.key, header.value); err != nil {
			return err
		}
	}

	contentType := "text/plain"
	body := message.TextBody
	if message.ContentType == ContentHTML {
		contentType, body = "text/html", message.HTMLBody
	}
	if len(delivery.Attachments) == 0 {
		if _, err := fmt.Fprintf(buffer, "Content-Type: %s; charset=UTF-8\r\nContent-Transfer-Encoding: base64\r\n\r\n", contentType); err != nil {
			return err
		}
		if err := writeBase64(buffer, strings.NewReader(body)); err != nil {
			return err
		}
		return buffer.Flush()
	}

	mixed := multipart.NewWriter(buffer)
	if _, err := fmt.Fprintf(buffer, "Content-Type: multipart/mixed; boundary=%q\r\n\r\n", mixed.Boundary()); err != nil {
		return err
	}
	bodyHeader := make(textproto.MIMEHeader)
	bodyHeader.Set("Content-Type", contentType+"; charset=UTF-8")
	bodyHeader.Set("Content-Transfer-Encoding", "base64")
	bodyPart, err := mixed.CreatePart(bodyHeader)
	if err != nil {
		return err
	}
	if err := writeBase64(bodyPart, strings.NewReader(body)); err != nil {
		return err
	}
	for _, attachment := range delivery.Attachments {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Type", attachment.MIMEType)
		header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": attachment.Filename}))
		header.Set("Content-Transfer-Encoding", "base64")
		part, err := mixed.CreatePart(header)
		if err != nil {
			return err
		}
		if err := writeBase64(part, attachment.Body); err != nil {
			return err
		}
	}
	if err := mixed.Close(); err != nil {
		return err
	}
	return buffer.Flush()
}

func writeBase64(target io.Writer, source io.Reader) error {
	encoder := base64.NewEncoder(base64.StdEncoding, &lineWriter{target: target, width: 76})
	_, copyErr := io.Copy(encoder, source)
	closeErr := encoder.Close()
	if _, err := io.WriteString(target, "\r\n"); err != nil {
		return errors.Join(copyErr, closeErr, err)
	}
	return errors.Join(copyErr, closeErr)
}

type lineWriter struct {
	target io.Writer
	width  int
	column int
}

func (w *lineWriter) Write(value []byte) (int, error) {
	written := 0
	for len(value) > 0 {
		remaining := w.width - w.column
		if remaining == 0 {
			if _, err := io.WriteString(w.target, "\r\n"); err != nil {
				return written, err
			}
			w.column = 0
			remaining = w.width
		}
		count := min(remaining, len(value))
		n, err := w.target.Write(value[:count])
		written += n
		w.column += n
		value = value[n:]
		if err != nil {
			return written, err
		}
		if n != count {
			return written, io.ErrShortWrite
		}
	}
	return written, nil
}

func formatAddress(address Address) string {
	return (&stdmail.Address{Name: address.Name, Address: address.Email}).String()
}

func formatAddresses(addresses []Address) string {
	result := make([]string, len(addresses))
	for index, address := range addresses {
		result[index] = formatAddress(address)
	}
	return strings.Join(result, ", ")
}
