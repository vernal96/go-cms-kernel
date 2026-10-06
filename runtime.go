package kernel

import (
	"context"
	"errors"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

type ModuleCode string

type ProfileCode string

type Profile struct {
	Code        ProfileCode
	Name        string
	Modules     []Module
	Params      []field.Definition
	EditorTabs  []field.EditorTab
	Templates   []template.Definition
	WidgetViews []widget.View
}

type Module interface {
	Code() ModuleCode
	Validate(context.Context, ModuleValidationContext) error
	Build(context.Context, ModuleContext) (ModuleRuntime, error)
}

type ModuleApplication interface {
	ModuleCode() ModuleCode
}

type ModuleDescriptor struct {
	Label       string
	Description string
}

type ModuleDescriptorProvider interface {
	ModuleDescriptor() ModuleDescriptor
}

type DependencyProvider interface {
	Dependencies() []ModuleCode
}

type ModuleRuntime interface {
	ModuleCode() ModuleCode
}

type RuntimeBuildFinalizer interface {
	FinalizeRuntimeBuild(context.Context) error
}

type RuntimeTransitionReason string

var ErrRuntimeTransitionBlocked = errors.New("runtime transition is blocked")

const (
	RuntimeTransitionProfileChange RuntimeTransitionReason = "profile_change"
	RuntimeTransitionSiteDelete    RuntimeTransitionReason = "site_delete"
)

type RuntimeTransition struct {
	Reason      RuntimeTransitionReason
	ScopeID     string
	FromProfile ProfileCode
	ToProfile   ProfileCode
}

type PreparedRuntimeTransition interface {
	Commit()
	Abort()
}

type RuntimeTransitionParticipant interface {
	PrepareRuntimeTransition(
		context.Context,
		RuntimeTransition,
	) (PreparedRuntimeTransition, error)
}

type RuntimeScope struct {
	siteID      string
	profileCode ProfileCode
	domain      string
	locale      string
	isPublic    bool
	settings    map[string]any
}

func NewSiteRuntimeScope(
	siteID string,
	profileCode ProfileCode,
	domain string,
	locale string,
	isPublic bool,
	settings map[string]any,
) RuntimeScope {
	return RuntimeScope{siteID: siteID, profileCode: profileCode, domain: domain, locale: locale, isPublic: isPublic, settings: cloneRuntimeSettings(settings)}
}

func NewRuntimeScope(
	siteID string,
	domain string,
	locale string,
	settings map[string]any,
) RuntimeScope {
	return RuntimeScope{
		siteID:   siteID,
		domain:   domain,
		locale:   locale,
		settings: cloneRuntimeSettings(settings),
	}
}

func (s RuntimeScope) SiteID() string { return s.siteID }

func (s RuntimeScope) ProfileCode() ProfileCode { return s.profileCode }

func (s RuntimeScope) Domain() string { return s.domain }

func (s RuntimeScope) Locale() string { return s.locale }

func (s RuntimeScope) IsPublic() bool { return s.isPublic }

func (s RuntimeScope) Settings() map[string]any {
	return cloneRuntimeSettings(s.settings)
}

func (s RuntimeScope) clone() RuntimeScope {
	return NewSiteRuntimeScope(s.siteID, s.profileCode, s.domain, s.locale, s.isPublic, s.settings)
}

func cloneRuntimeSettings(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = cloneRuntimeSettingValue(value)
	}
	return result
}

func cloneRuntimeSettingValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneRuntimeSettings(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = cloneRuntimeSettingValue(item)
		}
		return result
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}
