package mail

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"
)

type smtpSinkResult struct {
	recipients []string
	message    string
	err        error
}

func TestSMTPTransportDeliversMIMEMessageToSink(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	results := make(chan smtpSinkResult, 1)
	go serveSMTPSink(listener, results)

	host, portValue, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := NewSMTPTransport(SMTPConfig{Host: host, Port: port, Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	result, err := transport.Send(context.Background(), Delivery{
		Message: Message{
			RFCMessageID: "<audit@example.test>",
			From:         Address{Name: "CMS", Email: "sender@example.test"},
			To:           []Address{{Email: "to@example.test"}},
			CC:           []Address{{Email: "cc@example.test"}},
			BCC:          []Address{{Email: "bcc@example.test"}},
			Subject:      "Аудит SMTP",
			ContentType:  ContentText,
			TextBody:     "delivery-body",
			RequestedAt:  time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC),
		},
		Attachments: []DeliveryAttachment{{
			Attachment: Attachment{Filename: "evidence.txt", MIMEType: "text/plain"},
			Body:       strings.NewReader("attachment-body"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Driver != "smtp" || result.ResponseCode != "250" {
		t.Fatalf("delivery result = %#v", result)
	}

	select {
	case sink := <-results:
		if sink.err != nil {
			t.Fatal(sink.err)
		}
		if got := strings.Join(sink.recipients, ","); got != "to@example.test,cc@example.test,bcc@example.test" {
			t.Fatalf("envelope recipients = %q", got)
		}
		for _, fragment := range []string{
			"Message-ID: <audit@example.test>",
			"Subject: =?UTF-8?",
			"filename=evidence.txt",
		} {
			if !strings.Contains(sink.message, fragment) {
				t.Fatalf("SMTP message does not contain %q:\n%s", fragment, sink.message)
			}
		}
		if strings.Contains(strings.ToLower(sink.message), "bcc:") {
			t.Fatalf("SMTP message exposed Bcc header:\n%s", sink.message)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SMTP sink did not receive the message")
	}
}

func serveSMTPSink(listener net.Listener, results chan<- smtpSinkResult) {
	connection, err := listener.Accept()
	if err != nil {
		results <- smtpSinkResult{err: err}
		return
	}
	defer connection.Close()
	reader := bufio.NewReader(connection)
	writer := bufio.NewWriter(connection)
	write := func(response string) error {
		if _, err := writer.WriteString(response + "\r\n"); err != nil {
			return err
		}
		return writer.Flush()
	}
	if err := write("220 audit-sink ESMTP"); err != nil {
		results <- smtpSinkResult{err: err}
		return
	}

	result := smtpSinkResult{}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			results <- smtpSinkResult{err: err}
			return
		}
		command := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(command, "EHLO "):
			err = write("250-audit-sink\r\n250 8BITMIME")
		case strings.HasPrefix(command, "MAIL FROM:"):
			err = write("250 sender accepted")
		case strings.HasPrefix(command, "RCPT TO:"):
			result.recipients = append(result.recipients, strings.Trim(command[len("RCPT TO:"):], "<>"))
			err = write("250 recipient accepted")
		case command == "DATA":
			if err = write("354 end with dot"); err == nil {
				var message []byte
				message, err = readSMTPData(reader)
				result.message = string(message)
			}
			if err == nil {
				err = write("250 queued")
			}
		case command == "QUIT":
			if err = write("221 bye"); err == nil {
				results <- result
			}
			return
		default:
			err = fmt.Errorf("unexpected SMTP command %q", command)
		}
		if err != nil {
			results <- smtpSinkResult{err: err}
			return
		}
	}
}

func readSMTPData(reader *bufio.Reader) ([]byte, error) {
	var result strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == ".\r\n" {
			return []byte(result.String()), nil
		}
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		if _, err := io.WriteString(&result, line); err != nil {
			return nil, err
		}
	}
}
