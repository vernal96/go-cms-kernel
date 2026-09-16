package mail

import (
	"context"
	"errors"
	"io"
	"net"
	"net/textproto"
	"strconv"
)

func classifyDeliveryError(err error) *DeliveryError {
	if err == nil {
		return nil
	}
	var classified *DeliveryError
	if errors.As(err, &classified) {
		return classified
	}
	var protocol *textproto.Error
	if errors.As(err, &protocol) {
		return &DeliveryError{Retryable: protocol.Code >= 400 && protocol.Code < 500, Code: strconv.Itoa(protocol.Code), Err: err}
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return &DeliveryError{Retryable: true, Code: "context", Err: err}
	}
	var network net.Error
	if errors.As(err, &network) {
		return &DeliveryError{Retryable: true, Code: "network", Err: err}
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.ErrClosedPipe) {
		return &DeliveryError{Retryable: true, Code: "io", Err: err}
	}
	return &DeliveryError{Code: "permanent", Err: err}
}

func terminalDeliveryError(code string, err error) *DeliveryError {
	return &DeliveryError{Code: code, Err: err}
}
