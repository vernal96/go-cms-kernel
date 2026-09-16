package forms

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"reflect"
	"testing"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/security"
)

func TestMultiplePublicSubmission(t *testing.T) {
	for _, tc := range []struct {
		name   string
		json   string
		parts  []string
		count  int
		status int
	}{
		{name: "json-one", json: `["a@example.test"]`, count: 1, status: 200},
		{name: "json-many", json: `["b@example.test","a@example.test"]`, count: 2, status: 200},
		{name: "json-scalar", json: `"a@example.test"`, status: 422},
		{name: "json-invalid-item", json: `["a@example.test","invalid"]`, status: 422},
		{name: "multipart-one", parts: []string{"a@example.test"}, count: 1, status: 200},
		{name: "multipart-many", parts: []string{"b@example.test", "a@example.test"}, count: 2, status: 200},
		{name: "multipart-array", parts: []string{`["a@example.test"]`}, count: 1, status: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			detail := publicHTTPFormDetail()
			detail.Fields[2].Options = field.StringOptions{Multiple: true, MinItems: 1, MaxItems: 2}
			detail.Fields[3].Required = false
			repository := &repositoryStub{detail: detail}
			service, _ := publicHTTPService(t, repository)
			body := &bytes.Buffer{}
			contentType := "application/json"
			if tc.parts != nil {
				writer := multipart.NewWriter(body)
				for key, value := range map[string]string{MandatoryConsentCode: "true", MandatoryCaptchaCode: "valid"} {
					if err := writer.WriteField(key, value); err != nil {
						t.Fatal(err)
					}
				}
				for _, value := range tc.parts {
					if err := writer.WriteField("email", value); err != nil {
						t.Fatal(err)
					}
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
				contentType = writer.FormDataContentType()
			} else {
				body.WriteString(`{"values":{"privacy_consent":true,"captcha":"valid","email":` + tc.json + `}}`)
			}
			response := servePublicSubmit(t, service, body, contentType)
			if response.Code != tc.status {
				t.Fatalf("%d %s", response.Code, response.Body.String())
			}
			if response.Code != http.StatusOK {
				return
			}
			values := []ResultValue{}
			for _, value := range repository.record.Values {
				if value.FieldCode == "email" {
					values = append(values, value)
				}
			}
			if len(values) != tc.count {
				t.Fatalf("values %#v", values)
			}
			for i, value := range values {
				if !value.Multiple || value.Position != i {
					t.Fatalf("value %#v", value)
				}
			}
			if reflect.ValueOf(ResultFieldValue(values)).Kind() != reflect.Slice {
				t.Fatal("lost cardinality")
			}
		})
	}
}

func TestResultFieldValueAndMailPreserveHistoricalMultiplicity(t *testing.T) {
	integration := &mailIntegrationStub{}
	action := mailActionType{mail: integration, fieldTypes: formsFieldResolver()}
	values := []ResultValue{{FieldCode: "email", Multiple: true, Position: 0, Value: "person@example.test"}}
	_, err := action.Execute(context.Background(), ActionExecutionContext{Values: values}, json.RawMessage(`{"template_code":"feedback","values":{"email":"email"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(integration.input.Values["email"], []any{"person@example.test"}) {
		t.Fatalf("mapped %#v", integration.input.Values)
	}
	if got := ResultFieldValue([]ResultValue{{Multiple: true, Position: 1, Value: 2}, {Multiple: true, Position: 0, Value: 1}}); !reflect.DeepEqual(got, []any{1, 2}) {
		t.Fatalf("ordered %#v", got)
	}
	if got := ResultFieldValue([]ResultValue{{Value: "scalar"}}); got != "scalar" {
		t.Fatalf("scalar %#v", got)
	}
}

func TestInvalidListBoundsAreConfigurationErrors(t *testing.T) {
	err := validateFormField(FormField{FormID: 1, Code: "emails", Label: "Emails", Type: field.TypeEmail, Options: field.StringOptions{Multiple: true, MinItems: 3, MaxItems: 2}}, formsFieldResolver())
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("configuration error: %v", err)
	}
}

func TestChangingMultiplicityRevalidatesExistingMailMapping(t *testing.T) {
	repository := &repositoryStub{detail: publicHTTPFormDetail()}
	service, _ := publicHTTPService(t, repository)
	service.repository = &fieldUpdateRepositoryStub{repository}
	action := mailActionType{mail: &mailIntegrationStub{}, fieldTypes: formsFieldResolver()}
	if err := service.actions.Register(action); err != nil {
		t.Fatal(err)
	}
	repository.detail.Actions = []Action{{Code: "notify", ActionType: action.Code(), Trigger: Trigger{Type: TriggerSubmitted}, Config: json.RawMessage(`{"template_code":"feedback","values":{"email":"email"}}`)}}
	item := repository.detail.Fields[2]
	item.Options = field.StringOptions{Multiple: true}
	if _, err := service.UpdateField(context.Background(), security.User(1), repository.detail.Form.ID, item); !errors.Is(err, ErrConflict) {
		t.Fatalf("incompatible mapping accepted: %v", err)
	}
}

type fieldUpdateRepositoryStub struct {
	*repositoryStub
}

func (r *fieldUpdateRepositoryStub) FormDetail(context.Context, site.ID, FormID) (FormDetail, error) {
	return r.detail, nil
}
