package search

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

type engineFunc func(context.Context, Query) (Page, error)

func (f engineFunc) Search(ctx context.Context, query Query) (Page, error) { return f(ctx, query) }

type authorizerFunc func(context.Context, security.Actor, permission.Code) error

func (f authorizerFunc) Check(ctx context.Context, actor security.Actor, code permission.Code) error {
	return f(ctx, actor, code)
}

func TestServiceOwnsSiteAndChecksPermission(t *testing.T) {
	allowed := true
	calls := 0
	types := []resourcetype.Code{resourcetype.Page}
	service, err := NewService(7, engineFunc(func(ctx context.Context, query Query) (Page, error) {
		calls++
		if query.SiteID != 7 || query.Text != "Ресурс" || query.Page != 1 || query.PerPage != 20 || query.RouteTypes[0] != resourcetype.Page {
			t.Fatalf("query: %#v", query)
		}
		query.RouteTypes[0] = "mutated"
		return Page{}, nil
	}), authorizerFunc(func(_ context.Context, actor security.Actor, code permission.Code) error {
		if !actor.IsGuest() || code != permission.MustCode("core", "resource", permission.Read) {
			t.Fatalf("authorization: %#v %v", actor, code)
		}
		if !allowed {
			return security.ErrForbidden
		}
		return nil
	}), types)
	if err != nil {
		t.Fatal(err)
	}
	types[0] = "mutated"
	for range 2 {
		result, err := service.Search(context.Background(), security.Guest(), Input{Text: "  Ресурс  "})
		if err != nil || result.Items == nil || result.Pagination.Page != 1 {
			t.Fatalf("result: %#v %v", result, err)
		}
	}
	allowed = false
	if _, err := service.Search(context.Background(), security.Guest(), Input{Text: "Ресурс"}); !errors.Is(err, security.ErrForbidden) || calls != 2 {
		t.Fatalf("forbidden: %v; calls %d", err, calls)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := service.Search(ctx, security.Guest(), Input{Text: "Ресурс"}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestSearchHTTPValidationAndErrors(t *testing.T) {
	engineError := error(nil)
	calls := 0
	service, err := NewService(1, engineFunc(func(_ context.Context, _ Query) (Page, error) { calls++; return Page{}, engineError }), authorizerFunc(func(context.Context, security.Actor, permission.Code) error { return nil }), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"", "q=ab", "q=%20%20%20", "q=valid&page=0", "q=valid&per_page=51", "q=valid&page=no", "q=valid&page=", "q=valid&page=1&page=2", "q=valid&q=other", "q=valid&per_page=-1", "q=%00abc", "q=valid&unused=%zz"} {
		request := httptest.NewRequest(http.MethodGet, "/search?"+query, nil)
		request = request.WithContext(httptransport.WithActor(request.Context(), security.Guest()))
		response := httptest.NewRecorder()
		service.serveHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("%q: %d %s", query, response.Code, response.Body.String())
		}
	}
	if calls != 0 {
		t.Fatalf("invalid requests reached engine: %d", calls)
	}
	for _, test := range []struct {
		err    error
		status int
	}{{nil, 200}, {errors.New("database password must not escape"), 500}, {security.ErrForbidden, 403}} {
		engineError = test.err
		request := httptest.NewRequest(http.MethodGet, "/search?q=слово", nil)
		request = request.WithContext(httptransport.WithActor(request.Context(), security.Guest()))
		response := httptest.NewRecorder()
		service.serveHTTP(response, request)
		if response.Code != test.status || strings.Contains(response.Body.String(), "password") {
			t.Fatalf("response: %d %s", response.Code, response.Body.String())
		}
		if test.status == 200 {
			var page Page
			if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil || page.Items == nil || page.Pagination.PerPage != 20 {
				t.Fatalf("page: %#v %v", page, err)
			}
		}
	}
}

func TestNormalizeInputUnicodeAndOverflow(t *testing.T) {
	for _, input := range []Input{{Text: strings.Repeat("я", 201)}, {Text: "abc", Page: math.MaxInt, PerPage: 50}, {Text: "\xffabc"}} {
		if _, err := NormalizeInput(input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %#v", input)
		}
	}
	if _, err := NormalizeInput(Input{Text: strings.Repeat("я", 200)}); err != nil {
		t.Fatal(err)
	}
}
