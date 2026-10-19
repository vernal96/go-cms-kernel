package httptransport

import (
	"context"
	"log/slog"

	"github.com/go-chi/httplog/v3"
)

// RecordError attaches the server-side cause of a 5xx response to the request's
// access log. It does not change the response or emit a separate log record.
// Without request logging middleware it has no effect.
func RecordError(ctx context.Context, err error) {
	if ctx == nil || err == nil {
		return
	}
	httplog.SetAttrs(ctx, slog.String("error.message", err.Error()))
}
