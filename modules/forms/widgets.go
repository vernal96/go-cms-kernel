package forms

import (
	"context"
	"fmt"
	"net/url"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

var FormWidget = widget.NewRef("form")
var ResultsWidget = widget.NewRef("results")

type formWidget struct {
	service *Service
	results bool
}

func (r *Runtime) Widgets() []widget.Widget {
	return []widget.Widget{formWidget{service: r.service}, formWidget{service: r.service, results: true}}
}

func (w formWidget) Definition() widget.Definition {
	required, optional := true, false
	result := widget.Definition{
		Reference: FormWidget, Label: "Форма", Description: "Выводит форму текущего сайта",
		Fields:        []field.Definition{{Key: "form_id", Type: field.TypeInteger, Label: "Форма", Required: &required, Rules: []string{"min=1"}, Editor: "forms.form-picker"}},
		SummaryFields: []string{"form_id"},
	}
	if w.results {
		result.Reference, result.Label = ResultsWidget, "Результаты формы"
		result.Description = "Выводит публичные поля результатов формы постранично"
		result.Fields = append(result.Fields, field.Definition{Key: "per_page", Type: field.TypeInteger, Label: "На странице", Required: &optional, Rules: []string{"min=1", "max=100"}, Editor: "forms.results-page-size"})
	}
	return result
}

func (w formWidget) New(params map[string]any) (widget.Instance, error) {
	id, ok := params["form_id"].(int64)
	if !ok || id <= 0 {
		return nil, fmt.Errorf("%w: form_id is invalid", widget.ErrInvalidParams)
	}
	perPage := int64(20)
	if value, exists := params["per_page"]; exists && value != nil {
		var valid bool
		perPage, valid = value.(int64)
		if !valid || perPage < 1 || perPage > 100 {
			return nil, fmt.Errorf("%w: per_page is invalid", widget.ErrInvalidParams)
		}
	}
	return formWidgetInstance{widget: w, formID: FormID(id), perPage: int(perPage)}, nil
}

type formWidgetInstance struct {
	widget  formWidget
	formID  FormID
	perPage int
}

func (i formWidgetInstance) Render(ctx context.Context, input widget.RenderInput) (map[string]any, error) {
	s := i.widget.service
	if s == nil || int64(s.siteID) != input.Site.ID {
		return nil, ErrNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	form, err := s.repository.FormByID(ctx, s.siteID, i.formID)
	if err != nil {
		return nil, err
	}
	if !form.Enabled {
		return nil, ErrNotFound
	}
	root := "/forms/" + url.PathEscape(form.Code)
	if i.widget.results {
		page, err := s.repository.ListPublicResults(ctx, s.siteID, form.ID, PageQuery{Page: 1, PerPage: i.perPage})
		if err != nil {
			return nil, err
		}
		return map[string]any{"columns": page.Columns, "items": page.Items, "pagination": page.Pagination, "results_url": root + "/results"}, nil
	}
	detail, err := s.PublicForm(ctx, form.Code)
	if err != nil {
		return nil, err
	}
	schema, err := s.publicSchema(ctx, detail)
	if err != nil {
		return nil, err
	}
	return map[string]any{"form": schema, "submit_url": root + "/submit"}, nil
}

var _ widget.Provider = (*Runtime)(nil)
