package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

func publicHTTP(service *Service) httptransport.Builder {
	return httptransport.BuilderFunc(func(context.Context) (httptransport.Contribution, error) {
		if service == nil {
			return httptransport.Contribution{}, errors.New("search service is nil")
		}
		return httptransport.Contribution{Routes: func(registrar httptransport.Registrar) error {
			return registrar.Route(httptransport.Route{Name: "search.query", Method: http.MethodGet, Pattern: "/search", Handler: http.HandlerFunc(service.serveHTTP)})
		}}, nil
	})
}

func (s *Service) serveHTTP(response http.ResponseWriter, request *http.Request) {
	actor, exists := httptransport.ActorFromContext(request.Context())
	if !exists {
		httptransport.WriteJSONError(response, http.StatusInternalServerError, "internal_error", "request actor is unavailable")
		return
	}
	values, parseErr := url.ParseQuery(request.URL.RawQuery)
	var err error
	input := Input{Text: values.Get("q")}
	for name, target := range map[string]*int{"page": &input.Page, "per_page": &input.PerPage} {
		if raw, exists := values[name]; exists {
			if len(raw) != 1 {
				err = ErrInvalid
				break
			}
			*target, err = strconv.Atoi(raw[0])
			if err != nil || *target <= 0 {
				err = ErrInvalid
				break
			}
		}
	}
	if parseErr != nil || len(values["q"]) != 1 {
		err = ErrInvalid
	}
	var result Page
	if err == nil {
		result, err = s.Search(request.Context(), actor, input)
	}
	if err != nil {
		status, code, message := http.StatusInternalServerError, "internal_error", "search failed"
		switch {
		case errors.Is(err, ErrInvalid):
			status, code, message = http.StatusBadRequest, "invalid_query", "q must contain 3 to 200 characters; page must be positive; per_page must be between 1 and 50"
		case errors.Is(err, security.ErrUnauthenticated):
			status, code, message = http.StatusUnauthorized, "unauthenticated", "authentication required"
		case errors.Is(err, security.ErrForbidden):
			status, code, message = http.StatusForbidden, "forbidden", "resource access denied"
		}
		httptransport.WriteJSONError(response, status, code, message)
		return
	}
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(response).Encode(result)
}
