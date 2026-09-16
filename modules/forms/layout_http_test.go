package forms

import (
	"context"
	"encoding/json"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type layoutRepositoryStub struct {
	Repository
	placement LayoutPlacement
	siteID    site.ID
	formID    FormID
	deleted   LayoutNodeID
}

func (r *layoutRepositoryStub) FormDetail(context.Context, site.ID, FormID) (FormDetail, error) {
	return FormDetail{}, nil
}
func (r *layoutRepositoryStub) CreateField(_ context.Context, siteID site.ID, formID FormID, item FormField, placement LayoutPlacement) (FormField, LayoutNode, error) {
	r.siteID, r.formID, r.placement = siteID, formID, placement
	item.ID = 10
	return item, LayoutNode{ID: 20, Kind: LayoutField, FieldID: &item.ID, ParentID: placement.ParentID, Position: placement.Position}, nil
}
func (r *layoutRepositoryStub) CreateElement(_ context.Context, siteID site.ID, formID FormID, item Element, placement LayoutPlacement) (Element, LayoutNode, error) {
	r.siteID, r.formID, r.placement = siteID, formID, placement
	item.ID = 11
	return item, LayoutNode{ID: 21, Kind: LayoutElement, ElementID: &item.ID, ParentID: placement.ParentID, Position: placement.Position}, nil
}
func (r *layoutRepositoryStub) DeleteContainer(_ context.Context, siteID site.ID, formID FormID, id LayoutNodeID) error {
	r.siteID, r.formID, r.deleted = siteID, formID, id
	return nil
}

type layoutAuthorizer struct {
	deny    bool
	checked permission.Code
}

func (a *layoutAuthorizer) Check(_ context.Context, _ security.Actor, code permission.Code) error {
	a.checked = code
	if a.deny {
		return security.ErrForbidden
	}
	return nil
}

func TestLayoutHTTPPlacementAndAuthorization(t *testing.T) {
	elements, err := newElementCatalog()
	if err != nil {
		t.Fatal(err)
	}
	repo, authorizer := &layoutRepositoryStub{}, &layoutAuthorizer{}
	service := &Service{siteID: 5, repository: repo, fieldTypes: formsFieldResolver(), elements: elements, actions: newActionRegistry(), authorizer: authorizer}
	handler, err := NewManagementHTTPHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticated {
			req = req.WithContext(httptransport.WithActor(req.Context(), security.User(1)))
		} else {
			req = req.WithContext(httptransport.WithActor(req.Context(), security.Guest()))
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		return res
	}
	for _, item := range []struct{ path, body string }{
		{"fields", `{"code":"email","type":"email","label":"Email","parent_id":7,"position":2}`},
		{"elements", `{"code":"text","type":"text","config":{"content":"Hello"},"parent_id":7,"position":2}`},
	} {
		res := request(http.MethodPost, "/forms/9/"+item.path, item.body, true)
		if res.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", item.path, res.Code, res.Body.String())
		}
		if repo.siteID != 5 || repo.formID != 9 || repo.placement.ParentID == nil || *repo.placement.ParentID != 7 || repo.placement.Position != 2 {
			t.Fatalf("placement: %#v", repo)
		}
		if authorizer.checked != FormUpdatePermission {
			t.Fatalf("permission: %v", authorizer.checked)
		}
		authorizer.deny = true
		if res := request(http.MethodPost, "/forms/9/"+item.path, item.body, true); res.Code != http.StatusForbidden {
			t.Fatalf("forbidden: %d", res.Code)
		}
		authorizer.deny = false
	}
	if res := request(http.MethodDelete, "/forms/9/containers/7", "", true); res.Code != http.StatusNoContent || repo.deleted != 7 {
		t.Fatalf("delete: %d %s", res.Code, res.Body.String())
	}
	authorizer.deny = true
	repo.deleted = 0
	if res := request(http.MethodDelete, "/forms/9/containers/7", "", true); res.Code != http.StatusForbidden || repo.deleted != 0 {
		t.Fatalf("forbidden delete: %d", res.Code)
	}
	if res := request(http.MethodDelete, "/forms/9/containers/7", "", false); res.Code != http.StatusUnauthorized {
		t.Fatalf("guest delete: %d", res.Code)
	}
	authorizer.deny = false
	res := request(http.MethodGet, "/forms/9/editor", "", true)
	var editor editorResponse
	if res.Code != http.StatusOK || json.Unmarshal(res.Body.Bytes(), &editor) != nil || len(editor.ContainerTypes) != 2 {
		t.Fatalf("container metadata: %d %s", res.Code, res.Body.String())
	}
}
