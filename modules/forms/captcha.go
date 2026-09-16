package forms

import (
	"context"
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/vernal96/go-cms-kernel"
)

type Application struct{ Providers []CaptchaProvider }

func (Application) ModuleCode() kernel.ModuleCode { return ModuleCode }

// DevelopmentCaptchaProvider is intentionally test/development-only. Project
// composition must opt into it explicitly; production configuration must
// install a real provider instead of silently accepting every token.
type DevelopmentCaptchaProvider struct{ ExpectedToken string }

func (DevelopmentCaptchaProvider) Code() string { return "development" }
func (p DevelopmentCaptchaProvider) PublicConfig(context.Context) (map[string]any, error) {
	if strings.TrimSpace(p.ExpectedToken) == "" {
		return nil, errors.New("development CAPTCHA token is not configured")
	}
	return map[string]any{"development_only": true, "test_token": p.ExpectedToken}, nil
}
func (p DevelopmentCaptchaProvider) Verify(_ context.Context, input CaptchaInput) error {
	expected := strings.TrimSpace(p.ExpectedToken)
	if expected == "" || subtle.ConstantTimeCompare([]byte(input.Token), []byte(expected)) != 1 {
		return errors.New("CAPTCHA verification failed")
	}
	return nil
}

var _ kernel.ModuleApplication = Application{}
