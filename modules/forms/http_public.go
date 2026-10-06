package forms

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func newPublicHTTPBuilder(service *Service) httptransport.Builder {
	return httptransport.BuilderFunc(func(context.Context) (httptransport.Contribution, error) {
		if service == nil {
			return httptransport.Contribution{}, errors.New("Forms public service is nil")
		}
		h := &publicFormsHTTP{service: service}
		return httptransport.Contribution{Routes: func(registrar httptransport.Registrar) error {
			if err := registrar.Route(httptransport.Route{Name: "forms.schema", Method: http.MethodGet, Pattern: "/forms/{code}", Handler: http.HandlerFunc(h.schema)}); err != nil {
				return err
			}
			if err := registrar.Route(httptransport.Route{Name: "forms.results", Method: http.MethodGet, Pattern: "/forms/{code}/results", Handler: http.HandlerFunc(h.results)}); err != nil {
				return err
			}
			return registrar.Route(httptransport.Route{Name: "forms.submit", Method: http.MethodPost, Pattern: "/forms/{code}/submit", Handler: http.HandlerFunc(h.submit)})
		}}, nil
	})
}

type publicFormsHTTP struct{ service *Service }

func (h *publicFormsHTTP) schema(response http.ResponseWriter, request *http.Request) {
	detail, err := h.service.PublicForm(request.Context(), chi.URLParam(request, "code"))
	if err != nil {
		writePublicError(response, err)
		return
	}
	schema, err := h.service.publicSchema(request.Context(), detail)
	if err != nil {
		writePublicError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, schema)
}

func (h *publicFormsHTTP) submit(response http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(response, request.Body, h.service.limits.MaxRequestSize)
	ctx := request.Context()
	if h.service.limits.SubmissionTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.service.limits.SubmissionTimeout)
		defer cancel()
	}
	values := map[string]any{}
	var multipartInput map[string][]string
	uploads := []UploadInput{}
	closers := []io.Closer{}
	defer func() {
		for _, closer := range closers {
			_ = closer.Close()
		}
	}()
	mediaType := strings.ToLower(strings.TrimSpace(strings.Split(request.Header.Get("Content-Type"), ";")[0]))
	var err error
	if mediaType == "multipart/form-data" {
		if err = request.ParseMultipartForm(64 << 10); err == nil {
			defer request.MultipartForm.RemoveAll()
			multipartInput = request.MultipartForm.Value
			uploads, closers, err = multipartUploads(request.MultipartForm.File)
		}
	} else {
		var payload struct {
			Values map[string]any `json:"values"`
		}
		err = decodeJSONRequest(request, &payload)
		values = payload.Values
		if values == nil {
			values = map[string]any{}
		}
	}
	if err != nil {
		if errors.As(err, new(*http.MaxBytesError)) {
			writePublicError(response, ErrRequestTooLarge)
		} else {
			writePublicError(response, fmt.Errorf("%w: request payload is invalid", ErrInvalid))
		}
		return
	}
	actor, exists := httptransport.ActorFromContext(request.Context())
	if !exists {
		writePublicError(response, errors.New("request actor is unavailable"))
		return
	}
	_, err = h.service.Submit(ctx, actor, SubmitInput{FormCode: chi.URLParam(request, "code"), Values: values, MultipartValues: multipartInput, Uploads: uploads, UserAgent: request.UserAgent(), ClientAddress: clientAddress(request)})
	if err != nil {
		writePublicError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]bool{"success": true})
}

func multipartValues(source map[string][]string) map[string]any {
	result := make(map[string]any, len(source))
	for key, items := range source {
		values := make([]any, len(items))
		for index, item := range items {
			var decoded any
			if json.Unmarshal([]byte(item), &decoded) != nil {
				decoded = item
			}
			values[index] = decoded
		}
		if len(values) == 1 {
			result[key] = values[0]
		} else {
			result[key] = values
		}
	}
	return result
}

func multipartUploads(source map[string][]*multipart.FileHeader) ([]UploadInput, []io.Closer, error) {
	result := []UploadInput{}
	closers := []io.Closer{}
	for fieldCode, items := range source {
		for position, item := range items {
			body, err := item.Open()
			if err != nil {
				return nil, closers, err
			}
			closers = append(closers, body)
			result = append(result, UploadInput{FieldCode: fieldCode, Position: position, Filename: item.Filename, MIMEType: item.Header.Get("Content-Type"), Size: item.Size, Body: body})
		}
	}
	return result, closers, nil
}

func clientAddress(request *http.Request) string {
	value := strings.TrimSpace(request.RemoteAddr)
	if host, _, err := net.SplitHostPort(value); err == nil {
		return host
	}
	return value
}

func writePublicError(response http.ResponseWriter, err error) {
	var fields FieldValidationErrors
	var validators field.ValidationErrors
	switch {
	case errors.As(err, &validators):
		grouped := make(map[string][]field.ValidationError)
		for _, item := range validators {
			grouped[item.Key] = append(grouped[item.Key], item)
		}
		writeJSON(response, http.StatusUnprocessableEntity, map[string]any{"error": "validation_failed", "fields": grouped})
	case errors.As(err, &fields):
		writeJSON(response, http.StatusUnprocessableEntity, map[string]any{"error": "validation_failed", "fields": fields})
	case errors.Is(err, ErrNotFound):
		httptransport.WriteJSONError(response, http.StatusNotFound, "not_found", "form not found")
	case errors.Is(err, ErrRequestTooLarge):
		httptransport.WriteJSONError(response, http.StatusRequestEntityTooLarge, "request_too_large", "form request is too large")
	case errors.Is(err, ErrRateLimited):
		response.Header().Set("Retry-After", "60")
		httptransport.WriteJSONError(response, http.StatusTooManyRequests, "rate_limited", "too many submissions")
	case errors.Is(err, ErrRuntimeDraining):
		httptransport.WriteJSONError(response, http.StatusServiceUnavailable, "unavailable", "form submission is temporarily unavailable")
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrValidation):
		httptransport.WriteJSONError(response, http.StatusUnprocessableEntity, "validation_failed", "form submission is invalid")
	default:
		httptransport.WriteJSONError(response, http.StatusInternalServerError, "internal_error", "form submission failed")
	}
}

var _ = filesystem.VisibilityPublic

func normalizeMultipartLists(fields []FormField, values map[string]any, resolver field.TypeResolver) (map[string]any, error) {
	result := make(map[string]any, len(values))
	for key, value := range values {
		result[key] = value
	}
	for _, item := range fields {
		value, exists := result[item.Code]
		if !exists {
			continue
		}
		t, exists := resolver.FieldType(item.Type)
		if !exists {
			return nil, fmt.Errorf("unknown field type %q", item.Type)
		}
		compiled, err := t.Compile(field.CompileContext{Types: resolver}, item.Options)
		if err != nil {
			return nil, err
		}
		storage, ok := compiled.(field.StorageValueType)
		if !ok || !storage.Multiple() {
			continue
		}
		if _, array := value.([]any); !array {
			result[item.Code] = []any{value}
		}
	}
	return result, nil
}
