package forms

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type FormID int64
type FieldID int64
type ElementID int64
type LayoutNodeID int64
type StatusID int64
type ResultID int64
type ResultValueID int64
type ResultUploadID int64
type ActionID int64
type ActionExecutionID int64

type ElementTypeCode string
type LayoutKind string
type ContainerType string
type TriggerType string
type ExecutionStatus string

const (
	ModuleCode kernel.ModuleCode = "forms"

	ElementText         ElementTypeCode = "text"
	ElementHeading      ElementTypeCode = "heading"
	ElementImage        ElementTypeCode = "image"
	ElementSubmitButton ElementTypeCode = "submit_button"

	LayoutField     LayoutKind = "field"
	LayoutElement   LayoutKind = "element"
	LayoutContainer LayoutKind = "container"

	ContainerGroup ContainerType = "group"
	ContainerSlide ContainerType = "slide"

	TriggerSubmitted     TriggerType = "submitted"
	TriggerStatusChanged TriggerType = "status_changed"

	ExecutionPending   ExecutionStatus = "pending"
	ExecutionRunning   ExecutionStatus = "running"
	ExecutionRetryable ExecutionStatus = "retryable"
	ExecutionSucceeded ExecutionStatus = "succeeded"
	ExecutionFailed    ExecutionStatus = "failed"
)

var (
	ErrNotFound           = errors.New("forms item not found")
	ErrConflict           = errors.New("forms item conflict")
	ErrInvalid            = errors.New("forms item is invalid")
	ErrValidation         = errors.New("forms submission validation failed")
	ErrRuntimeDraining    = errors.New("forms runtime is draining")
	ErrActiveExecutions   = errors.New("forms has active action executions")
	ErrRateLimited        = errors.New("forms submission rate limit exceeded")
	ErrRequestTooLarge    = errors.New("forms request is too large")
	ErrActionUnavailable  = errors.New("forms action type is unavailable")
	ErrExecutionBusy      = errors.New("forms action execution lease is active")
	ErrCaptchaUnavailable = errors.New("forms CAPTCHA provider is unavailable")
)

type Form struct {
	ID          FormID           `json:"id"`
	SiteID      site.ID          `json:"site_id"`
	Code        string           `json:"code"`
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Enabled     bool             `json:"enabled"`
	CreatedAt   time.Time        `json:"created_at"`
	UpdatedAt   time.Time        `json:"updated_at"`
	CreatedBy   *security.UserID `json:"created_by,omitempty"`
	UpdatedBy   *security.UserID `json:"updated_by,omitempty"`
}

type FormField struct {
	ID             FieldID            `json:"id"`
	FormID         FormID             `json:"form_id"`
	Code           string             `json:"code"`
	Type           field.TypeCode     `json:"type"`
	Label          string             `json:"label"`
	Required       bool               `json:"required"`
	Rules          []string           `json:"rules"`
	Options        any                `json:"options,omitempty"`
	Editor         field.EditorCode   `json:"editor,omitempty"`
	VisibleWhen    *field.VisibleWhen `json:"visible_when,omitempty"`
	ResultLabel    string             `json:"result_label"`
	ShowOnSite     bool               `json:"show_on_site"`
	ShowInResults  bool               `json:"show_in_results"`
	ResultPosition int                `json:"result_position"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

func (f FormField) Definition() field.Definition {
	required := f.Required
	return field.Definition{
		Key: f.Code, Type: f.Type, Label: f.Label, Required: &required,
		Rules: append([]string(nil), f.Rules...), Options: f.Options,
		Editor: f.Editor, VisibleWhen: cloneVisibleWhen(f.VisibleWhen),
	}
}

func (f FormField) EffectiveResultLabel() string {
	if f.ResultLabel != "" {
		return f.ResultLabel
	}
	return f.Label
}

func cloneVisibleWhen(source *field.VisibleWhen) *field.VisibleWhen {
	if source == nil {
		return nil
	}
	result := *source
	return &result
}

type Element struct {
	ID        ElementID       `json:"id"`
	FormID    FormID          `json:"form_id"`
	Code      string          `json:"code"`
	Type      ElementTypeCode `json:"type"`
	Config    json.RawMessage `json:"config"`
	CreatedAt time.Time       `json:"created_at"`
	UpdatedAt time.Time       `json:"updated_at"`
}

// LayoutPlacement specifies the insertion point among a parent's children.
type LayoutPlacement struct {
	ParentID *LayoutNodeID `json:"parent_id"`
	Position int           `json:"position"`
}

type ContainerTypeMetadata struct {
	Code  ContainerType `json:"code"`
	Label string        `json:"label"`
}

func AvailableContainerTypes() []ContainerTypeMetadata {
	return []ContainerTypeMetadata{{ContainerGroup, "Группа"}, {ContainerSlide, "Слайд"}}
}

type LayoutNode struct {
	ID            LayoutNodeID    `json:"id"`
	FormID        FormID          `json:"form_id"`
	ParentID      *LayoutNodeID   `json:"parent_id,omitempty"`
	Kind          LayoutKind      `json:"kind"`
	FieldID       *FieldID        `json:"field_id,omitempty"`
	ElementID     *ElementID      `json:"element_id,omitempty"`
	ContainerType ContainerType   `json:"container_type,omitempty"`
	Position      int             `json:"position"`
	Config        json.RawMessage `json:"config,omitempty"`
}

type Status struct {
	ID        StatusID  `json:"id"`
	FormID    FormID    `json:"form_id"`
	Code      string    `json:"code"`
	Name      string    `json:"name"`
	Color     string    `json:"color"`
	Position  int       `json:"position"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Trigger struct {
	Type TriggerType `json:"type"`
	From string      `json:"from_status,omitempty"`
	To   string      `json:"to_status,omitempty"`
}

type Action struct {
	ID         ActionID        `json:"id"`
	FormID     FormID          `json:"form_id"`
	Code       string          `json:"code"`
	Name       string          `json:"name"`
	Enabled    bool            `json:"enabled"`
	Trigger    Trigger         `json:"trigger"`
	ActionType string          `json:"action_type"`
	Config     json.RawMessage `json:"config"`
	Position   int             `json:"position"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type Result struct {
	ID            ResultID         `json:"id"`
	SiteID        site.ID          `json:"site_id"`
	FormID        FormID           `json:"form_id"`
	FormCode      string           `json:"form_code"`
	FormName      string           `json:"form_name"`
	StatusID      StatusID         `json:"status_id"`
	StatusCode    string           `json:"status_code"`
	StatusName    string           `json:"status_name"`
	StatusColor   string           `json:"status_color"`
	UserID        *security.UserID `json:"user_id,omitempty"`
	UserAgent     string           `json:"user_agent"`
	ClientAddress string           `json:"client_address,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

type ResultValue struct {
	Multiple    bool              `json:"multiple"`
	ID          ResultValueID     `json:"id"`
	ResultID    ResultID          `json:"result_id"`
	FieldID     *FieldID          `json:"field_id,omitempty"`
	FieldCode   string            `json:"field_code"`
	FieldLabel  string            `json:"field_label"`
	ResultLabel string            `json:"result_label"`
	FieldType   field.TypeCode    `json:"field_type"`
	StorageKind field.StorageKind `json:"storage_kind"`
	Position    int               `json:"position"`
	Value       any               `json:"value"`
}

type ResultUpload struct {
	ID             ResultUploadID `json:"id"`
	ResultID       ResultID       `json:"result_id"`
	FieldID        *FieldID       `json:"field_id,omitempty"`
	FieldCode      string         `json:"field_code"`
	Position       int            `json:"position"`
	Filename       string         `json:"filename"`
	MIMEType       string         `json:"mime_type"`
	Size           int64          `json:"size"`
	Checksum       string         `json:"checksum_sha256"`
	SpoolReference string         `json:"-"`
	SpoolDeletedAt *time.Time     `json:"spool_deleted_at,omitempty"`
}

type ActionExecution struct {
	ID                ActionExecutionID `json:"id"`
	SiteID            site.ID           `json:"site_id"`
	ResultID          ResultID          `json:"result_id"`
	ActionID          *ActionID         `json:"action_id,omitempty"`
	ActionCode        string            `json:"action_code"`
	ActionName        string            `json:"action_name"`
	ActionType        string            `json:"action_type"`
	Trigger           Trigger           `json:"trigger"`
	Config            json.RawMessage   `json:"-"`
	Status            ExecutionStatus   `json:"status"`
	AttemptCount      int               `json:"attempt_count"`
	SafeError         string            `json:"safe_error"`
	ExternalReference string            `json:"external_reference"`
	StartedAt         *time.Time        `json:"started_at,omitempty"`
	FinishedAt        *time.Time        `json:"finished_at,omitempty"`
	CreatedAt         time.Time         `json:"created_at"`
	UpdatedAt         time.Time         `json:"updated_at"`
}

type FormDetail struct {
	Form     Form         `json:"form"`
	Fields   []FormField  `json:"fields"`
	Elements []Element    `json:"elements"`
	Layout   []LayoutNode `json:"layout"`
	Statuses []Status     `json:"statuses"`
	Actions  []Action     `json:"actions"`
}

type FormSummaryPage struct {
	Items []Form
	Total int
}

type ResultSummary struct {
	Result
	Values map[string]any `json:"values"`
}

type ResultSummaryPage struct {
	Items []ResultSummary
	Total int
}

type ResultDetail struct {
	Result            Result            `json:"result"`
	Values            []ResultValue     `json:"values"`
	Uploads           []ResultUpload    `json:"uploads"`
	Executions        []ActionExecution `json:"action_executions"`
	AvailableStatuses []Status          `json:"available_statuses,omitempty"`
}

type PageQuery struct {
	Page    int
	PerPage int
	Search  string
}

type ResultQuery struct {
	PageQuery
	FormID   FormID
	StatusID StatusID
	DateFrom *time.Time
	DateTo   *time.Time
}

type FieldValidationErrors map[string][]string

func (e FieldValidationErrors) Error() string { return ErrValidation.Error() }

type UploadInput struct {
	FieldCode string
	Position  int
	Filename  string
	MIMEType  string
	Size      int64
	Body      io.Reader
}

type CaptchaInput struct {
	SiteID        site.ID
	FormCode      string
	Token         string
	ClientAddress string
}

type CaptchaProvider interface {
	Code() string
	PublicConfig(context.Context) (map[string]any, error)
	Verify(context.Context, CaptchaInput) error
}

type CaptchaOptions struct {
	Provider string `json:"provider,omitempty"`
}

type ConsentOptions struct {
	Text string `json:"text,omitempty"`
	URL  string `json:"url,omitempty"`
}

type UploadOptions struct {
	MIMETypes   []string `json:"mime_types,omitempty"`
	MaxFileSize int64    `json:"max_file_size,omitempty"`
	Multiple    bool     `json:"multiple,omitempty"`
	MaxFiles    int      `json:"max_files,omitempty"`
}
