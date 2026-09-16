package forms

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
)

type PublicResultColumn struct {
	Code  string         `json:"code"`
	Label string         `json:"label"`
	Type  field.TypeCode `json:"type"`
}
type PublicResult struct {
	CreatedAt time.Time      `json:"created_at"`
	Values    map[string]any `json:"values"`
}
type PublicResultPagination struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
	Pages   int `json:"pages"`
}
type PublicResultsPage struct {
	Columns    []PublicResultColumn   `json:"columns"`
	Items      []PublicResult         `json:"items"`
	Pagination PublicResultPagination `json:"pagination"`
}

func ValidatePublicPage(query PageQuery) error {
	if query.Page < 1 || query.PerPage < 1 || query.PerPage > 100 || query.Page-1 > int(^uint(0)>>1)/query.PerPage {
		return fmt.Errorf("%w: pagination is invalid", ErrInvalid)
	}
	return nil
}

func (s *Service) PublicResults(ctx context.Context, code string, query PageQuery) (PublicResultsPage, error) {
	if err := ValidatePublicPage(query); err != nil {
		return PublicResultsPage{}, err
	}
	if err := validateCode(code, "form"); err != nil {
		return PublicResultsPage{}, ErrNotFound
	}
	form, err := s.repository.FormByCode(ctx, s.siteID, code, true)
	if err != nil {
		return PublicResultsPage{}, err
	}
	return s.repository.ListPublicResults(ctx, s.siteID, form.ID, query)
}

func (h *publicFormsHTTP) results(response http.ResponseWriter, request *http.Request) {
	query := PageQuery{Page: 1, PerPage: 20}
	for key, target := range map[string]*int{"page": &query.Page, "per_page": &query.PerPage} {
		if values, exists := request.URL.Query()[key]; exists {
			if len(values) != 1 {
				writePublicError(response, ErrInvalid)
				return
			}
			value, err := strconv.Atoi(values[0])
			if err != nil {
				writePublicError(response, ErrInvalid)
				return
			}
			*target = value
		}
	}
	result, err := h.service.PublicResults(request.Context(), chi.URLParam(request, "code"), query)
	if err != nil {
		writePublicError(response, err)
		return
	}
	response.Header().Set("Cache-Control", "no-store")
	writeJSON(response, http.StatusOK, result)
}
