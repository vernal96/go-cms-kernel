package management

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type ResourceOption struct {
	ID           resource.ID       `json:"id"`
	ParentID     *resource.ID      `json:"parent_id"`
	Type         resourcetype.Code `json:"type"`
	DisplayTitle string            `json:"display_title"`
	Path         *string           `json:"path"`
}

type ResourceOptions struct {
	Items []ResourceOption `json:"items"`
}

type ResourceLookup struct {
	Items      []ResourceOption `json:"items"`
	Pagination Pagination       `json:"pagination"`
}

func (m *Resources) ResourceLookup(ctx context.Context, actor security.Actor, siteID site.ID, search string, page, perPage int) (ResourceLookup, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return ResourceLookup{}, err
	}
	page, perPage, err := normalizePagination(page, perPage)
	if err != nil {
		return ResourceLookup{}, err
	}
	items, err := m.resourceRepo.ListBySite(ctx, siteID)
	if err != nil {
		return ResourceLookup{}, fmt.Errorf("list resource lookup: %w", err)
	}
	query := strings.ToLower(strings.TrimSpace(search))
	options := make([]ResourceOption, 0, len(items))
	for _, item := range items {
		if query != "" && !strings.Contains(strings.ToLower(item.Title), query) && !strings.Contains(strings.ToLower(item.MenuTitle), query) && (item.Path == nil || !strings.Contains(strings.ToLower(*item.Path), query)) {
			continue
		}
		title := strings.TrimSpace(item.MenuTitle)
		if title == "" {
			title = item.Title
		}
		options = append(options, ResourceOption{ID: item.ID, ParentID: item.ParentID, Type: item.Type, DisplayTitle: title, Path: item.Path})
	}
	total := len(options)
	start := (page - 1) * perPage
	if start > total {
		start = total
	}
	end := start + perPage
	if end > total {
		end = total
	}
	return ResourceLookup{Items: options[start:end], Pagination: Pagination{Page: page, PerPage: perPage, Total: total}}, nil
}

func (m *Resources) ResourceOptions(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
) (ResourceOptions, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessEdit); err != nil {
		return ResourceOptions{}, err
	}
	tree, err := m.resources.Tree(ctx, actor, siteID)
	if err != nil {
		return ResourceOptions{}, err
	}
	items := make([]ResourceOption, 0)
	appendResourceOptions(&items, tree)
	return ResourceOptions{Items: items}, nil
}

type Menu struct {
	Items []MenuItem `json:"items"`
}

type MenuItem struct {
	ID       resource.ID `json:"id"`
	Title    string      `json:"title"`
	URL      string      `json:"url"`
	Children []MenuItem  `json:"children"`
}

func (m *Resources) Menu(
	ctx context.Context,
	actor security.Actor,
	siteID site.ID,
) (Menu, error) {
	if err := m.requireSite(ctx, actor, siteID, ResourceReadPermission, SiteAccessView); err != nil {
		return Menu{}, err
	}
	if _, exists := m.sites.RuntimeByID(siteID); !exists {
		return Menu{}, site.ErrNotFound
	}
	items, err := m.resourceRepo.ListBySite(ctx, siteID)
	if err != nil {
		return Menu{}, fmt.Errorf("list menu resources: %w", err)
	}
	now := time.Now().UTC()
	byID := make(map[resource.ID]resource.Resource, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	visible := make(map[resource.ID]resource.Resource, len(items))
	urls := make(map[resource.ID]string, len(items))
	for _, item := range items {
		if !item.InMenu || !resourcePublishedAt(item, now) {
			continue
		}
		url, ok := menuURL(item, byID, now)
		if !ok {
			continue
		}
		visible[item.ID] = item
		urls[item.ID] = url
	}
	children := make(map[resource.ID][]resource.Resource)
	roots := make([]resource.Resource, 0)
	for _, item := range visible {
		if item.ParentID == nil {
			roots = append(roots, item)
			continue
		}
		if _, parentVisible := visible[*item.ParentID]; parentVisible {
			children[*item.ParentID] = append(children[*item.ParentID], item)
		}
	}
	sortResources := func(items []resource.Resource) {
		sort.Slice(items, func(i, j int) bool {
			if items[i].Sort != items[j].Sort {
				return items[i].Sort < items[j].Sort
			}
			return items[i].ID < items[j].ID
		})
	}
	sortResources(roots)
	for id := range children {
		sortResources(children[id])
	}
	var project func([]resource.Resource) []MenuItem
	project = func(source []resource.Resource) []MenuItem {
		result := make([]MenuItem, len(source))
		for index, item := range source {
			title := strings.TrimSpace(item.MenuTitle)
			if title == "" {
				title = item.Title
			}
			result[index] = MenuItem{
				ID: item.ID, Title: title, URL: urls[item.ID],
				Children: project(children[item.ID]),
			}
		}
		return result
	}
	return Menu{Items: project(roots)}, nil
}

func resourcePublishedAt(item resource.Resource, now time.Time) bool {
	return item.DeletedAt == nil && item.IsPublic &&
		(item.PublishedAt == nil || !now.Before(item.PublishedAt.UTC())) &&
		(item.UnpublishedAt == nil || now.Before(item.UnpublishedAt.UTC()))
}

func menuURL(item resource.Resource, byID map[resource.ID]resource.Resource, now time.Time) (string, bool) {
	switch item.Type {
	case resourcetype.Page, resourcetype.Library:
		if item.Path == nil {
			return "", false
		}
		return *item.Path, true
	case resourcetype.Link:
		if item.ExternalURL == nil {
			return "", false
		}
		return *item.ExternalURL, true
	case resourcetype.ResourceLink:
		if item.TargetResourceID == nil {
			return "", false
		}
		target, exists := byID[*item.TargetResourceID]
		if !exists || target.SiteID != item.SiteID || target.Path == nil || !resourcePublishedAt(target, now) {
			return "", false
		}
		return *target.Path, true
	default:
		return "", false
	}
}

func (m authorization) requireSite(
	ctx context.Context,
	actor security.Actor,
	id site.ID,
	code permission.Code,
	action SiteAccessAction,
) error {
	if id <= 0 {
		return fmt.Errorf("%w: invalid site id", ErrValidation)
	}
	if err := m.authorizer.Check(ctx, actor, code); err != nil {
		return err
	}
	return m.policy.Check(ctx, actor, id, action)
}

func (m authorization) allowed(
	ctx context.Context,
	actor security.Actor,
	code permission.Code,
) (bool, error) {
	err := m.authorizer.Check(ctx, actor, code)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrUnauthenticated) {
		return false, nil
	}
	return false, err
}

func (m *Sites) sitePermissions(
	ctx context.Context,
	actor security.Actor,
) (PermissionSet, error) {
	codes := []permission.Code{SiteReadPermission, SiteCreatePermission, SiteUpdatePermission, SiteDeletePermission}
	allowed, err := m.allowedPermissions(ctx, actor, codes)
	if err != nil {
		return PermissionSet{}, err
	}
	return PermissionSet{Read: allowed[codes[0]], Create: allowed[codes[1]], Update: allowed[codes[2]], Delete: allowed[codes[3]]}, nil
}

func (m *Sites) siteCapabilities(
	ctx context.Context,
	actor security.Actor,
	items []site.Site,
	viewScope *SiteAccessScope,
) (map[site.ID]SiteCapabilities, error) {
	result := make(map[site.ID]SiteCapabilities, len(items))
	actions := []SiteAccessAction{SiteAccessView, SiteAccessEdit, SiteAccessDelete}
	for actionIndex, action := range actions {
		var scope SiteAccessScope
		if actionIndex == 0 && viewScope != nil {
			scope = *viewScope
		} else {
			var err error
			scope, err = m.policy.Scope(ctx, actor, action)
			if err != nil {
				return nil, err
			}
		}
		allowed := make(map[site.ID]struct{}, len(scope.SiteIDs))
		for _, id := range scope.SiteIDs {
			allowed[id] = struct{}{}
		}
		for _, item := range items {
			_, includes := allowed[item.ID]
			if !scope.All && !includes {
				continue
			}
			capabilities := result[item.ID]
			switch actionIndex {
			case 0:
				capabilities.View = true
			case 1:
				capabilities.View = true
				capabilities.Edit = true
			case 2:
				capabilities.View = true
				capabilities.Edit = true
				capabilities.Delete = true
			}
			result[item.ID] = capabilities
		}
	}
	return result, nil
}

func normalizePagination(page, perPage int) (int, int, error) {
	if page == 0 {
		page = 1
	}
	if perPage == 0 {
		perPage = 10
	}
	if page < 1 || perPage < 1 || perPage > 100 {
		return 0, 0, fmt.Errorf("%w: invalid pagination", ErrValidation)
	}
	return page, perPage, nil
}

func siteDTO(item site.Site, capabilities SiteCapabilities) SiteDTO {
	settings := make(map[string]any, len(item.Settings))
	for key, value := range item.Settings {
		settings[key] = value
	}
	return SiteDTO{
		ID:           item.ID,
		ProfileCode:  item.ProfileCode,
		Domain:       item.Domain,
		Locale:       item.Locale,
		Settings:     settings,
		IsPublic:     item.IsPublic,
		Capabilities: capabilities,
	}
}

func treeItem(runtime *site.Runtime, item resource.Child, canCreate bool) ResourceTreeItem {
	displayTitle := strings.TrimSpace(item.MenuTitle)
	if displayTitle == "" {
		displayTitle = item.Title
	}
	icon := "document"
	if runtime == nil {
		if item.Type == resourcetype.Link || item.Type == resourcetype.ResourceLink {
			icon = "link"
		} else if item.Type == resourcetype.Library {
			icon = "collection"
		}
	} else {
		if resourceType, exists := runtime.Profile().Registry().ResourceType(item.Type); exists {
			icon = iconOrDefault(resourceType.Metadata().Capabilities.DefaultIcon)
		}
		if item.Template != nil {
			if templateRuntime, exists := runtime.Profile().Template(*item.Template); exists {
				if templateIcon, allowed := allowedIcon(templateRuntime.Definition().Icon); allowed {
					icon = templateIcon
				}
			}
		}
	}
	return ResourceTreeItem{
		ID:              item.ID,
		Version:         item.Version,
		ParentID:        item.ParentID,
		TemplateCode:    item.Template,
		Icon:            icon,
		Title:           item.Title,
		MenuTitle:       item.MenuTitle,
		DisplayTitle:    displayTitle,
		Sort:            item.Sort,
		Deleted:         item.DeletedAt != nil,
		Published:       isPublished(item),
		InMenu:          item.InMenu,
		DeletedAt:       item.DeletedAt,
		HasChildren:     item.HasChildren,
		CanCreateChild:  canCreate && item.DeletedAt == nil,
		CanTransferSite: item.CanTransferSite,
	}
}

func isPublished(item resource.Child) bool {
	if item.DeletedAt != nil || !item.IsPublic {
		return false
	}
	now := time.Now().UTC()
	return (item.PublishedAt == nil || !now.Before(item.PublishedAt.UTC())) &&
		(item.UnpublishedAt == nil || now.Before(item.UnpublishedAt.UTC()))
}

func resourceDTO(item resource.Resource) ResourceDTO {
	fields := make(map[string]any, len(item.Fields))
	for key, value := range item.Fields {
		fields[key] = value
	}
	typeSettings := make(map[string]any, len(item.TypeSettings))
	for key, value := range item.TypeSettings {
		typeSettings[key] = value
	}
	return ResourceDTO{
		ImageMediaID:     item.ImageMediaID,
		ID:               item.ID,
		SiteID:           item.SiteID,
		Version:          item.Version,
		ParentID:         item.ParentID,
		Type:             item.Type,
		TemplateCode:     item.Template,
		Title:            item.Title,
		MenuTitle:        item.MenuTitle,
		Slug:             item.Slug,
		Path:             item.Path,
		Annotation:       item.Annotation,
		ContentType:      item.ContentType,
		Content:          item.Content,
		TargetResourceID: item.TargetResourceID,
		ExternalURL:      item.ExternalURL,
		IsPublic:         item.IsPublic,
		IsSearchable:     item.IsSearchable,
		InMenu:           item.InMenu,
		InSitemap:        item.InSitemap,
		Sort:             item.Sort,
		PublishedAt:      item.PublishedAt,
		UnpublishedAt:    item.UnpublishedAt,
		Deleted:          item.DeletedAt != nil,
		DeletedAt:        item.DeletedAt,
		Fields:           fields,
		TypeSettings:     typeSettings,
		Widgets:          resourceWidgets(item.Widgets),
	}
}

func resourceWidgets(source []widget.Binding) []ResourceWidget {
	result := make([]ResourceWidget, len(source))
	for index, binding := range source {
		bindings := widget.CloneParamBindings(binding.ParamBindings)
		if bindings == nil {
			bindings = widget.ParamBindings{}
		}
		params := make(map[string]any, len(binding.Params))
		for key, value := range binding.Params {
			params[key] = value
		}
		result[index] = ResourceWidget{
			ID: binding.ID, Code: binding.Code, Area: binding.Area, Position: binding.Position,
			View: widget.PublicView(binding.Presentation.View), Columns: binding.Presentation.Columns,
			MarginTop: binding.Presentation.MarginTop, MarginBottom: binding.Presentation.MarginBottom,
			Enabled: binding.Presentation.Enabled, Params: params, ParamBindings: bindings,
		}
	}
	return result
}

func appendResourceOptions(target *[]ResourceOption, nodes []resource.Node) {
	for _, node := range nodes {
		item := node.Resource
		if item.DeletedAt != nil {
			continue
		}
		displayTitle := strings.TrimSpace(item.MenuTitle)
		if displayTitle == "" {
			displayTitle = item.Title
		}
		*target = append(*target, ResourceOption{
			ID:           item.ID,
			ParentID:     item.ParentID,
			Type:         item.Type,
			DisplayTitle: displayTitle,
			Path:         item.Path,
		})
		appendResourceOptions(target, node.Children)
	}
}

func iconOrDefault(icon string) string {
	if normalized, allowed := allowedIcon(icon); allowed {
		return normalized
	}
	return "document"
}

func allowedIcon(icon string) (string, bool) {
	icon = strings.ToLower(strings.TrimSpace(icon))
	switch icon {
	case "document", "link", "folder", "tickets", "collection":
		return icon, true
	default:
		return "", false
	}
}

func cloneAnyMap(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		switch typed := value.(type) {
		case map[string]any:
			result[key] = cloneAnyMap(typed)
		case []any:
			items := make([]any, len(typed))
			copy(items, typed)
			result[key] = items
		case []string:
			result[key] = append([]string(nil), typed...)
		default:
			result[key] = typed
		}
	}
	return result
}

func validationError(err error) error {
	if errors.Is(err, security.ErrUnauthenticated) || errors.Is(err, security.ErrForbidden) {
		return err
	}
	if errors.Is(err, site.ErrConflict) || errors.Is(err, site.ErrNotFound) ||
		errors.Is(err, resource.ErrConflict) || errors.Is(err, resource.ErrRouteConflict) || errors.Is(err, resource.ErrNotFound) ||
		errors.Is(err, resource.ErrInvalidTree) || errors.Is(err, resource.ErrCrossSiteReference) ||
		errors.Is(err, resource.ErrIncompatibleTargetSite) {
		return err
	}
	var fieldErrors field.ValidationErrors
	if errors.As(err, &fieldErrors) {
		fields := make([]FieldValidationError, len(fieldErrors))
		for index, item := range fieldErrors {
			fields[index] = FieldValidationError{
				Key: item.Key, Code: string(item.Code), Params: item.Params,
			}
		}
		return ValidationError{
			Message: "request data is invalid",
			Fields:  fields,
		}
	}
	if errors.Is(err, site.ErrInvalid) || errors.Is(err, resource.ErrInvalid) ||
		errors.Is(err, resource.ErrInvalidReference) {
		return fmt.Errorf("%w: request data is invalid", ErrValidation)
	}
	return err
}

func (m authorization) allowedPermissions(ctx context.Context, actor security.Actor, codes []permission.Code) (map[permission.Code]bool, error) {
	allowed, err := m.authorizer.Allowed(ctx, actor, codes)
	if errors.Is(err, security.ErrForbidden) || errors.Is(err, security.ErrUnauthenticated) {
		return map[permission.Code]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := make(map[permission.Code]bool, len(allowed))
	for _, code := range allowed {
		result[code] = true
	}
	return result, nil
}
