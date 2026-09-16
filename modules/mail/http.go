package mail

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type variableDTO struct {
	Key      string          `json:"key"`
	Type     field.TypeCode  `json:"type"`
	Label    string          `json:"label"`
	Required bool            `json:"required"`
	Rules    []string        `json:"rules"`
	Options  json.RawMessage `json:"options,omitempty"`
}

type templatePayload struct {
	Code        string               `json:"code"`
	Name        string               `json:"name"`
	Enabled     bool                 `json:"enabled"`
	From        AddressTemplate      `json:"from"`
	To          []AddressTemplate    `json:"to"`
	CC          []AddressTemplate    `json:"cc"`
	BCC         []AddressTemplate    `json:"bcc"`
	ReplyTo     *AddressTemplate     `json:"reply_to"`
	Subject     string               `json:"subject"`
	ContentType ContentType          `json:"content_type"`
	TextBody    string               `json:"text_body"`
	HTMLBody    string               `json:"html_body"`
	Attachments []AttachmentTemplate `json:"attachments"`
	Variables   []variableDTO        `json:"variables"`
}

type templateResponse struct {
	Template
	Variables []variableDTO `json:"variables"`
}

type paginationDTO struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

func NewHTTPHandler(service *Service) (http.Handler, error) {
	if service == nil {
		return nil, errors.New("mail service is nil")
	}
	handler := &mailHTTP{service: service}
	router := chi.NewRouter()
	router.Get("/templates", handler.listTemplates)
	router.Get("/variables", handler.siteVariables)
	router.Post("/templates", handler.createTemplate)
	router.Get("/templates/{templateID}", handler.getTemplate)
	router.Patch("/templates/{templateID}", handler.updateTemplate)
	router.Patch("/templates/{templateID}/enabled", handler.setTemplateEnabled)
	router.Delete("/templates/{templateID}", handler.deleteTemplate)
	router.Post("/preview", handler.preview)
	router.Post("/send", handler.send)
	router.Get("/send/templates", handler.sendTemplates)
	router.Get("/messages", handler.listMessages)
	router.Get("/messages/{messageID}", handler.getMessage)
	router.Delete("/messages/{messageID}", handler.deleteMessage)
	return httptransport.RequireAuthenticated(router), nil
}

func (h *mailHTTP) setTemplateEnabled(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	id, err := pathID[TemplateID](request, "templateID")
	if err != nil {
		writeMailError(response, err)
		return
	}
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if err := decodeJSONRequest(request, &payload); err != nil {
		writeMailError(response, err)
		return
	}
	item, err := service.SetTemplateEnabled(request.Context(), actor, id, payload.Enabled)
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		ID        TemplateID `json:"id"`
		Enabled   bool       `json:"enabled"`
		UpdatedAt time.Time  `json:"updated_at"`
	}{ID: item.ID, Enabled: item.Enabled, UpdatedAt: item.UpdatedAt})
}

type mailHTTP struct{ service *Service }

func (h *mailHTTP) listTemplates(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	query, err := pageQuery(request)
	if err != nil {
		writeMailError(response, err)
		return
	}
	page, err := service.ListTemplates(request.Context(), actor, query)
	if err != nil {
		writeMailError(response, err)
		return
	}
	items := make([]templateResponse, len(page.Items))
	for index, item := range page.Items {
		items[index], err = toTemplateResponse(item)
		if err != nil {
			writeMailError(response, err)
			return
		}
	}
	writeJSON(response, http.StatusOK, struct {
		Items      []templateResponse `json:"items"`
		Pagination paginationDTO      `json:"pagination"`
	}{items, paginationDTO{query.Page, query.PerPage, page.Total}})
}

func (h *mailHTTP) getTemplate(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	id, err := pathID[TemplateID](request, "templateID")
	if err != nil {
		writeMailError(response, err)
		return
	}
	item, err := service.Template(request.Context(), actor, id)
	if err != nil {
		writeMailError(response, err)
		return
	}
	result, err := toTemplateResponse(item)
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (h *mailHTTP) createTemplate(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	payload, err := decodeTemplatePayload(request)
	if err != nil {
		writeMailError(response, err)
		return
	}
	item, err := payload.template()
	if err != nil {
		writeMailError(response, err)
		return
	}
	created, err := service.CreateTemplate(request.Context(), actor, item)
	if err != nil {
		writeMailError(response, err)
		return
	}
	result, err := toTemplateResponse(created)
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusCreated, result)
}

func (h *mailHTTP) updateTemplate(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	id, err := pathID[TemplateID](request, "templateID")
	if err != nil {
		writeMailError(response, err)
		return
	}
	payload, err := decodeTemplatePayload(request)
	if err != nil {
		writeMailError(response, err)
		return
	}
	item, err := payload.template()
	if err != nil {
		writeMailError(response, err)
		return
	}
	item.ID = id
	updated, err := service.UpdateTemplate(request.Context(), actor, item)
	if err != nil {
		writeMailError(response, err)
		return
	}
	result, err := toTemplateResponse(updated)
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (h *mailHTTP) deleteTemplate(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	id, err := pathID[TemplateID](request, "templateID")
	if err == nil {
		err = service.DeleteTemplate(request.Context(), actor, id)
	}
	if err != nil {
		writeMailError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

type renderRequest struct {
	TemplateID TemplateID     `json:"template_id"`
	Values     map[string]any `json:"values"`
}

func (h *mailHTTP) preview(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	var input renderRequest
	if err := decodeJSONRequest(request, &input); err != nil {
		writeMailError(response, err)
		return
	}
	result, err := service.Preview(request.Context(), actor, input.TemplateID, input.Values)
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (h *mailHTTP) send(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	var input renderRequest
	if err := decodeJSONRequest(request, &input); err != nil {
		writeMailError(response, err)
		return
	}
	result, err := service.QueueManual(request.Context(), actor, ManualSendInput{TemplateID: input.TemplateID, Values: input.Values})
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusAccepted, result)
}

func (h *mailHTTP) sendTemplates(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	query, err := pageQuery(request)
	if err != nil {
		writeMailError(response, err)
		return
	}
	page, err := service.SendTemplates(request.Context(), actor, query)
	if err != nil {
		writeMailError(response, err)
		return
	}
	items := make([]templateResponse, len(page.Items))
	for index, item := range page.Items {
		items[index], err = toTemplateResponse(item)
		if err != nil {
			writeMailError(response, err)
			return
		}
	}
	writeJSON(response, http.StatusOK, struct {
		Items      []templateResponse `json:"items"`
		Pagination paginationDTO      `json:"pagination"`
	}{items, paginationDTO{query.Page, query.PerPage, page.Total}})
}

func (h *mailHTTP) listMessages(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	query, err := messageQuery(request)
	if err != nil {
		writeMailError(response, err)
		return
	}
	page, err := service.ListMessages(request.Context(), actor, query)
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Items      []MessageSummary `json:"items"`
		Pagination paginationDTO    `json:"pagination"`
	}{page.Items, paginationDTO{query.Page, query.PerPage, page.Total}})
}

func (h *mailHTTP) siteVariables(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	items, err := service.SiteVariables(request.Context(), actor)
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, struct {
		Items         []site.TemplateVariable `json:"items"`
		UploadStorage filesystem.Code         `json:"upload_storage"`
		UploadPath    string                  `json:"upload_path"`
	}{Items: items, UploadStorage: service.EditorConfig().UploadStorage, UploadPath: service.EditorConfig().UploadPath})
}

func (h *mailHTTP) getMessage(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	id, err := pathID[MessageID](request, "messageID")
	if err != nil {
		writeMailError(response, err)
		return
	}
	result, err := service.Message(request.Context(), actor, id)
	if err != nil {
		writeMailError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, result)
}

func (h *mailHTTP) deleteMessage(response http.ResponseWriter, request *http.Request) {
	actor, service, ok := h.request(response, request)
	if !ok {
		return
	}
	id, err := pathID[MessageID](request, "messageID")
	if err == nil {
		err = service.DeleteMessage(request.Context(), actor, id)
	}
	if err != nil {
		writeMailError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (h *mailHTTP) request(response http.ResponseWriter, request *http.Request) (security.Actor, *Service, bool) {
	actor, exists := httptransport.ActorFromContext(request.Context())
	if !exists {
		httptransport.WriteJSONError(response, http.StatusInternalServerError, "internal_error", "request actor is unavailable")
		return security.Actor{}, nil, false
	}
	return actor, h.service, true
}

func decodeTemplatePayload(request *http.Request) (templatePayload, error) {
	var payload templatePayload
	err := decodeJSONRequest(request, &payload)
	return payload, err
}

func decodeJSONRequest(request *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 8<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("%w: JSON payload is invalid", ErrInvalid)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing JSON data", ErrInvalid)
	}
	return nil
}

func (p templatePayload) template() (Template, error) {
	variables := make([]field.Definition, len(p.Variables))
	for index, variable := range p.Variables {
		definition, err := variable.definition()
		if err != nil {
			return Template{}, err
		}
		variables[index] = definition
	}
	return Template{Code: p.Code, Name: p.Name, Enabled: p.Enabled, From: p.From, To: p.To, CC: p.CC, BCC: p.BCC, ReplyTo: p.ReplyTo, Subject: p.Subject, ContentType: p.ContentType, TextBody: p.TextBody, HTMLBody: p.HTMLBody, Attachments: p.Attachments, Variables: variables}, nil
}

func (v variableDTO) definition() (field.Definition, error) {
	options, err := field.DecodeOptionsJSON(v.Type, v.Options)
	if err != nil {
		return field.Definition{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	required := v.Required
	return field.Definition{Key: v.Key, Type: v.Type, Label: v.Label, Required: &required, Rules: append([]string{}, v.Rules...), Options: options}, nil
}

func toTemplateResponse(item Template) (templateResponse, error) {
	variables := make([]variableDTO, len(item.Variables))
	for index, definition := range item.Variables {
		value, err := toVariableDTO(definition)
		if err != nil {
			return templateResponse{}, err
		}
		variables[index] = value
	}
	return templateResponse{Template: item, Variables: variables}, nil
}

func toVariableDTO(definition field.Definition) (variableDTO, error) {
	options, err := field.EncodeOptionsJSON(definition.Options)
	if err != nil {
		return variableDTO{}, err
	}
	return variableDTO{Key: definition.Key, Type: definition.Type, Label: definition.Label, Required: definition.Required != nil && *definition.Required, Rules: append([]string{}, definition.Rules...), Options: options}, nil
}

func pageQuery(request *http.Request) (PageQuery, error) {
	query := PageQuery{}
	for key, target := range map[string]*int{"page": &query.Page, "per_page": &query.PerPage} {
		if raw := request.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil {
				return PageQuery{}, fmt.Errorf("%w: pagination is invalid", ErrInvalid)
			}
			*target = value
		}
	}
	return normalizePage(query)
}

func messageQuery(request *http.Request) (MessageQuery, error) {
	page, err := pageQuery(request)
	if err != nil {
		return MessageQuery{}, err
	}
	query := MessageQuery{PageQuery: page, Status: MessageStatus(strings.TrimSpace(request.URL.Query().Get("status"))), TemplateCode: request.URL.Query().Get("template_code"), Recipient: request.URL.Query().Get("recipient")}
	for key, target := range map[string]**time.Time{"date_from": &query.DateFrom, "date_to": &query.DateTo} {
		raw := strings.TrimSpace(request.URL.Query().Get(key))
		if raw == "" {
			continue
		}
		value, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			return MessageQuery{}, fmt.Errorf("%w: %s is invalid", ErrInvalid, key)
		}
		value = value.UTC()
		*target = &value
	}
	return query, nil
}

func pathID[T ~int64](request *http.Request, key string) (T, error) {
	value, err := strconv.ParseInt(chi.URLParam(request, key), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%w: ID is invalid", ErrInvalid)
	}
	return T(value), nil
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func writeMailError(response http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, security.ErrUnauthenticated):
		response.Header().Set("WWW-Authenticate", "Bearer")
		httptransport.WriteJSONError(response, http.StatusUnauthorized, "unauthenticated", "authentication required")
	case errors.Is(err, security.ErrForbidden):
		httptransport.WriteJSONError(response, http.StatusForbidden, "forbidden", "mail access is forbidden")
	case errors.Is(err, ErrNotFound):
		httptransport.WriteJSONError(response, http.StatusNotFound, "not_found", "mail item not found")
	case errors.Is(err, ErrConflict), errors.Is(err, ErrRuntimeDraining):
		httptransport.WriteJSONError(response, http.StatusConflict, "conflict", "mail item conflicts with its current state")
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrTemplateDisabled), errors.Is(err, ErrNoRecipients), errors.Is(err, ErrSenderNotAllowed):
		httptransport.WriteJSONError(response, http.StatusUnprocessableEntity, "validation_failed", err.Error())
	default:
		httptransport.WriteJSONError(response, http.StatusInternalServerError, "internal_error", "mail operation failed")
	}
}
