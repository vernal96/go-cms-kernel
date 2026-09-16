package forms

import (
	"context"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

type CreateFormInput struct {
	Form    Form
	Consent FormField
	Captcha FormField
	Submit  Element
	Status  Status
}

type SubmissionRecord struct {
	Result  Result
	Values  []ResultValue
	Uploads []ResultUpload
	Actions []Action
}

type ResultStatusChange struct {
	SiteID       site.ID
	ResultID     ResultID
	FromStatusID StatusID
	ToStatusID   StatusID
	Actions      []Action
}

type ExecutionWork struct {
	Execution ActionExecution
	Result    Result
	Values    []ResultValue
	Uploads   []ResultUpload
}

type Repository interface {
	ListForms(context.Context, site.ID, PageQuery) (FormSummaryPage, error)
	FormByID(context.Context, site.ID, FormID) (Form, error)
	FormByCode(context.Context, site.ID, string, bool) (Form, error)
	FormDetail(context.Context, site.ID, FormID) (FormDetail, error)
	FormDetailByCode(context.Context, site.ID, string, bool) (FormDetail, error)
	CreateForm(context.Context, CreateFormInput) (FormDetail, error)
	UpdateForm(context.Context, Form) (Form, error)
	SetFormEnabled(context.Context, site.ID, FormID, bool, *security.UserID) (Form, error)
	DeleteForm(context.Context, site.ID, FormID) ([]string, error)

	CreateField(context.Context, site.ID, FormID, FormField, LayoutPlacement) (FormField, LayoutNode, error)
	UpdateField(context.Context, site.ID, FormField) (FormField, error)
	DeleteField(context.Context, site.ID, FormID, FieldID) error
	CreateElement(context.Context, site.ID, FormID, Element, LayoutPlacement) (Element, LayoutNode, error)
	UpdateElement(context.Context, site.ID, Element) (Element, error)
	DeleteElement(context.Context, site.ID, FormID, ElementID) error
	CreateContainer(context.Context, site.ID, FormID, LayoutNode) (LayoutNode, error)
	DeleteContainer(context.Context, site.ID, FormID, LayoutNodeID) error
	ReplaceLayout(context.Context, site.ID, FormID, []LayoutNode) ([]LayoutNode, error)

	CreateStatus(context.Context, site.ID, FormID, Status) (Status, error)
	UpdateStatus(context.Context, site.ID, Status) (Status, error)
	DeleteStatus(context.Context, site.ID, FormID, StatusID) error

	CreateAction(context.Context, site.ID, FormID, Action) (Action, error)
	UpdateAction(context.Context, site.ID, Action) (Action, error)
	DeleteAction(context.Context, site.ID, FormID, ActionID) error

	CreateResult(context.Context, SubmissionRecord) (ResultDetail, error)
	ListResults(context.Context, site.ID, ResultQuery, []string) (ResultSummaryPage, error)
	ListPublicResults(context.Context, site.ID, FormID, PageQuery) (PublicResultsPage, error)
	ResultDetail(context.Context, site.ID, ResultID) (ResultDetail, error)
	ChangeResultStatus(context.Context, ResultStatusChange) (ResultDetail, error)
	DeleteResult(context.Context, site.ID, ResultID) ([]string, error)

	ClaimExecution(context.Context, site.ID, ActionExecutionID, int) (ExecutionWork, bool, error)
	FinishExecution(context.Context, ActionExecutionID, ExecutionStatus, string, string) error
	HasActiveExecutions(context.Context, site.ID) (bool, error)
	ResultHasActiveSubmittedExecutions(context.Context, site.ID, ResultID) (bool, error)
	MarkUploadSpoolDeleted(context.Context, site.ID, ResultID, []string) error
	MarkUploadSpoolReferencesDeleted(context.Context, site.ID, []string) error
	MarkAllUploadSpoolDeleted(context.Context, site.ID) error
	ActiveSpoolReferences(context.Context, site.ID, []string) (map[string]struct{}, error)
}

type RepositoryClock interface {
	Now(context.Context) (time.Time, error)
}
