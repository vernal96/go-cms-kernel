package forms

import (
	"context"
	"encoding/json"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestElementRegistrationIsScopedSealedAndExposedByHTTP(t *testing.T) {
	repository := &repositoryStub{detail: publicHTTPFormDetail()}
	service, _ := publicHTTPService(t, repository)
	service.repository = extensionRepository{repository}
	runtime := &Runtime{service: service, actions: service.actions}
	element := ElementDefinition{Description: ElementTypeMetadata{Code: "example.notice", Label: "Notice", EditorCode: "example.notice-editor", Fields: []field.ConfigField{{Key: "text", Label: "Text", Type: field.TypeString, Required: true}, {Key: "count", Label: "Count", Type: field.TypeInteger}}}}
	if err := runtime.RegisterElementType(element); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RegisterElementType(element); err == nil {
		t.Fatal("duplicate accepted")
	}
	other, err := newElementCatalog()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := other.Type(element.Code()); ok {
		t.Fatal("element leaked into another site")
	}
	if err := runtime.FinalizeRuntimeBuild(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := runtime.RegisterElementType(ElementDefinition{Description: ElementTypeMetadata{Code: "late", Label: "Late"}}); err == nil {
		t.Fatal("late registration accepted")
	}
	if err := element.ValidateConfig(json.RawMessage(`{"text":"hello","count":3}`)); err != nil {
		t.Fatal(err)
	}
	if err := element.ValidateConfig(json.RawMessage(`{"text":"hello","count":"invalid"}`)); err == nil {
		t.Fatal("invalid count accepted")
	}
	handler, err := NewManagementHTTPHandler(service)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/forms/9/editor", nil)
	request = request.WithContext(httptransport.WithActor(request.Context(), security.User(1)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var metadata editorResponse
	if err := json.Unmarshal(response.Body.Bytes(), &metadata); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range metadata.ElementTypes {
		if item.Code == element.Code() {
			found = true
			if item.EditorCode != "example.notice-editor" || len(item.Fields) != 2 {
				t.Fatalf("element=%+v", item)
			}
		}
	}
	if !found {
		t.Fatal("custom element missing from editor metadata")
	}
	for _, item := range metadata.FieldTypes {
		if item.Code == field.TypeInteger {
			if item.Editor != "int" || len(item.Options) != 4 || item.Options[0].Key != "step" {
				t.Fatalf("integer metadata=%+v", item)
			}
			return
		}
	}
	t.Fatal("integer field metadata is missing")
}

type extensionRepository struct{ *repositoryStub }

func (r extensionRepository) FormDetail(_ context.Context, _ site.ID, _ FormID) (FormDetail, error) {
	return r.detail, nil
}
