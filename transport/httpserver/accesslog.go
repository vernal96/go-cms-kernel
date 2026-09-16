package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httplog/v3"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

const accessLogMessage = "HTTP request completed"

type accessLogHandler struct {
	next slog.Handler
}

func accessLogger(logger *slog.Logger) *slog.Logger {
	return slog.New(accessLogHandler{next: logger.Handler()})
}

func (h accessLogHandler) Enabled(
	ctx context.Context,
	level slog.Level,
) bool {
	if level <= slog.LevelInfo {
		return h.next.Enabled(ctx, slog.LevelInfo) ||
			h.next.Enabled(ctx, slog.LevelWarn)
	}
	return h.next.Enabled(ctx, level)
}

func (h accessLogHandler) Handle(
	ctx context.Context,
	record slog.Record,
) error {
	level := record.Level
	sanitized := slog.NewRecord(
		record.Time,
		level,
		accessLogMessage,
		record.PC,
	)
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "http.response.status_code" &&
			attr.Value.Kind() == slog.KindInt64 {
			switch status := attr.Value.Int64(); {
			case status >= http.StatusInternalServerError:
				level = slog.LevelError
			case status >= http.StatusBadRequest:
				level = slog.LevelWarn
			default:
				level = slog.LevelInfo
			}
			sanitized.Level = level
		}
		if attr.Key == "error.message" &&
			attr.Value.Kind() == slog.KindString &&
			strings.HasPrefix(attr.Value.String(), "panic:") {
			attr.Value = slog.StringValue("panic recovered")
		}
		sanitized.AddAttrs(attr)
		return true
	})
	if !h.next.Enabled(ctx, level) {
		return nil
	}
	return h.next.Handle(ctx, sanitized)
}

func (h accessLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return accessLogHandler{next: h.next.WithAttrs(attrs)}
}

func (h accessLogHandler) WithGroup(name string) slog.Handler {
	return accessLogHandler{next: h.next.WithGroup(name)}
}

func accessLogOptions() *httplog.Options {
	schema := *httplog.SchemaOTEL
	schema.RequestURL = ""
	schema.RequestHost = ""
	schema.RequestScheme = ""
	schema.RequestProto = ""
	schema.RequestHeaders = ""
	schema.RequestBody = ""
	schema.RequestBytesUnread = ""
	schema.RequestUserAgent = ""
	schema.RequestReferer = ""
	schema.ResponseHeaders = ""
	schema.ResponseBody = ""

	return &httplog.Options{
		Level:              slog.LevelDebug,
		Schema:             &schema,
		RecoverPanics:      true,
		LogRequestHeaders:  []string{},
		LogResponseHeaders: []string{},
	}
}

func requestLogMetadata(next http.Handler) http.Handler {
	return http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		defer func() {
			attrs := []slog.Attr{
				slog.String(
					"http.request.id",
					chimiddleware.GetReqID(request.Context()),
				),
			}
			routeContext := chi.RouteContext(request.Context())
			if routeContext != nil {
				if route := routeContext.RoutePattern(); route != "" {
					attrs = append(attrs, slog.String("http.route", route))
				}
			}
			httplog.SetAttrs(request.Context(), attrs...)
		}()

		next.ServeHTTP(response, request)
	})
}

func requestActorLogAttributes(next http.Handler) http.Handler {
	return http.HandlerFunc(func(
		response http.ResponseWriter,
		request *http.Request,
	) {
		actor, exists := httptransport.ActorFromContext(request.Context())
		if exists {
			attrs := []slog.Attr{
				slog.String("actor.kind", actorKind(actor)),
			}
			if userID, isUser := actor.UserID(); isUser {
				attrs = append(
					attrs,
					slog.Int64("actor.user.id", int64(userID)),
				)
			}
			httplog.SetAttrs(request.Context(), attrs...)
		}
		next.ServeHTTP(response, request)
	})
}

func actorKind(actor security.Actor) string {
	switch actor.Kind() {
	case security.ActorGuest:
		return "guest"
	case security.ActorUser:
		return "user"
	case security.ActorSystem:
		return "system"
	default:
		return "unknown"
	}
}
