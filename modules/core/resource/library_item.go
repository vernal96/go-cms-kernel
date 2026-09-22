package resource

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/security"
)

type LibraryItem struct {
	ID             ID
	SiteID         site.ID
	Version        int64
	LibraryID      ID
	Template       *template.Code
	ContentType    *string
	Title          string
	Slug           string
	Annotation     string
	Content        string
	ImageMediaID   *media.ID
	IsPublic       bool
	IsSearchable   bool
	PublishedAt    *time.Time
	UnpublishedAt  *time.Time
	Fields         map[string]any
	FieldValues    []field.StoredValue
	Widgets        []widget.Binding
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CreatedBy      *security.UserID
	UpdatedBy      *security.UserID
	DeletedAt      *time.Time
	DeletedBy      *security.UserID
	FileReferences map[string]file.ID
}

type CreateLibraryItemInput struct {
	SiteID        site.ID
	LibraryID     ID
	Template      *template.Code
	ContentType   *string
	Title         string
	Slug          string
	Annotation    string
	Content       string
	ImageMediaID  *media.ID
	IsPublic      *bool
	IsSearchable  *bool
	PublishedAt   *time.Time
	UnpublishedAt *time.Time
	Fields        map[string]any
}

type UpdateLibraryItemInput struct {
	ID              ID
	ExpectedVersion int64
	Template        *template.Code
	ContentType     *string
	Title           string
	Slug            string
	Annotation      string
	Content         string
	ImageMediaID    *media.ID
	IsPublic        bool
	IsSearchable    bool
	PublishedAt     *time.Time
	UnpublishedAt   *time.Time
	Fields          map[string]any
}

type LibraryItemQuery struct {
	SiteID     site.ID
	LibraryID  ID
	Filters    []FilterCondition
	Sort       []Sort
	Cursor     string
	Limit      int
	Search     string
	PublicOnly bool
	Deleted    *bool
}

type LibraryItemPage struct {
	Items      []LibraryItem
	NextCursor string
}

type LibraryItemRepository interface {
	CreateLibraryItem(context.Context, *security.UserID, LibraryItem, bool) (LibraryItem, error)
	LibraryItemByID(context.Context, ID) (LibraryItem, error)
	UpdateLibraryItem(context.Context, *security.UserID, LibraryItem, LibraryItem, bool) (LibraryItem, error)
	SoftDeleteLibraryItem(context.Context, *security.UserID, ID) error
	RestoreLibraryItem(context.Context, *security.UserID, ID) error
	DeleteLibraryItem(context.Context, ID) error
	MoveLibraryItem(context.Context, *security.UserID, ID, ID, int64, bool) (LibraryItem, error)
	QueryLibraryItems(context.Context, LibraryItemQuery) (LibraryItemPage, error)
	LibraryItemTemplateCodes(context.Context, site.ID, ID) ([]template.Code, error)
	LibraryItemWidgetCodes(context.Context, site.ID, ID) ([]widget.Code, error)
	ResolveLibraryItemRoute(context.Context, site.ID, string) (LibraryItem, Resource, error)
}

type LibraryService struct {
	repository LibraryItemRepository
	common     *Service
}

func NewLibraryService(repository LibraryItemRepository, common *Service) (*LibraryService, error) {
	if repository == nil || common == nil {
		return nil, errors.New("library item dependencies are nil")
	}
	return &LibraryService{repository: repository, common: common}, nil
}

func (s *LibraryService) Create(ctx context.Context, actor security.Actor, input CreateLibraryItemInput) (LibraryItem, error) {
	ctx, finishHooks, hookErr := s.common.beginMutation(ctx, actor, OperationCreate)
	if hookErr != nil {
		return LibraryItem{}, hookErr
	}
	defer finishHooks()

	if err := validateContext(ctx, "library item create"); err != nil {
		return LibraryItem{}, err
	}
	if err := s.common.authorizer.Check(ctx, actor, createPermission); err != nil {
		return LibraryItem{}, err
	}
	library, runtime, err := s.library(ctx, input.SiteID, input.LibraryID)
	if err != nil {
		return LibraryItem{}, err
	}
	templateCode := cloneTemplateCode(input.Template)
	if templateCode == nil {
		if value, ok := library.TypeSettings["default_item_template"].(string); ok && value != "" {
			code := template.Code(value)
			templateCode = &code
		}
	}
	item := LibraryItem{
		SiteID: input.SiteID, LibraryID: input.LibraryID, Template: templateCode,
		ContentType: cloneString(input.ContentType), Title: input.Title, Slug: input.Slug,
		Annotation: input.Annotation, Content: input.Content, ImageMediaID: cloneMediaID(input.ImageMediaID),
		IsPublic: boolDefault(input.IsPublic, true), IsSearchable: boolDefault(input.IsSearchable, true),
		PublishedAt: cloneTime(input.PublishedAt), UnpublishedAt: cloneTime(input.UnpublishedAt),
		Fields: cloneMap(input.Fields), CreatedBy: actor.AuditUserID(), UpdatedBy: actor.AuditUserID(),
	}
	if item.Slug == "" {
		item.Slug = GenerateSlug(item.Title)
	}
	item, err = s.normalize(ctx, actor, item, runtime, nil)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	created, err := s.repository.CreateLibraryItem(ctx, actor.AuditUserID(), item, revisionPolicyFor(runtime).LibraryItems)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("create library item: %w", err)
	}
	return cloneLibraryItem(created), nil
}

func (s *LibraryService) Get(ctx context.Context, actor security.Actor, id ID) (LibraryItem, error) {
	if err := validateContext(ctx, "library item get"); err != nil {
		return LibraryItem{}, err
	}
	if err := s.common.authorizer.Check(ctx, actor, readPermission); err != nil {
		return LibraryItem{}, err
	}
	item, err := s.repository.LibraryItemByID(ctx, id)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("get library item %d: %w", id, err)
	}
	return cloneLibraryItem(item), nil
}

func (s *LibraryService) Update(ctx context.Context, actor security.Actor, input UpdateLibraryItemInput) (LibraryItem, error) {
	ctx, finishHooks, hookErr := s.common.beginMutation(ctx, actor, OperationUpdate)
	if hookErr != nil {
		return LibraryItem{}, hookErr
	}
	defer finishHooks()

	if err := validateContext(ctx, "library item update"); err != nil {
		return LibraryItem{}, err
	}
	if err := s.common.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return LibraryItem{}, err
	}
	current, err := s.repository.LibraryItemByID(ctx, input.ID)
	if err != nil {
		return LibraryItem{}, err
	}
	if input.ExpectedVersion <= 0 || current.Version != input.ExpectedVersion {
		return LibraryItem{}, ErrConflict
	}
	_, runtime, err := s.library(ctx, current.SiteID, current.LibraryID)
	if err != nil {
		return LibraryItem{}, err
	}
	item := current
	item.Template = cloneTemplateCode(input.Template)
	item.ContentType = cloneString(input.ContentType)
	item.Title, item.Slug, item.Annotation, item.Content = input.Title, input.Slug, input.Annotation, input.Content
	item.ImageMediaID = cloneMediaID(input.ImageMediaID)
	item.IsPublic, item.IsSearchable = input.IsPublic, input.IsSearchable
	item.PublishedAt, item.UnpublishedAt = cloneTime(input.PublishedAt), cloneTime(input.UnpublishedAt)
	item.Fields = cloneMap(input.Fields)
	item.UpdatedBy = actor.AuditUserID()
	if item.Slug == "" {
		item.Slug = GenerateSlug(item.Title)
	}
	item, err = s.normalize(ctx, actor, item, runtime, current.FileReferences)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	updated, err := s.repository.UpdateLibraryItem(ctx, actor.AuditUserID(), current, item, revisionPolicyFor(runtime).LibraryItems)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("update library item: %w", err)
	}
	return cloneLibraryItem(updated), nil
}

func (s *LibraryService) Move(ctx context.Context, actor security.Actor, id, targetLibraryID ID, expectedVersion int64) (LibraryItem, error) {
	ctx, finishHooks, hookErr := s.common.beginMutation(ctx, actor, OperationMove)
	if hookErr != nil {
		return LibraryItem{}, hookErr
	}
	defer finishHooks()

	if err := s.common.authorizer.Check(ctx, actor, updatePermission); err != nil {
		return LibraryItem{}, err
	}
	item, err := s.repository.LibraryItemByID(ctx, id)
	if err != nil {
		return LibraryItem{}, err
	}
	if expectedVersion <= 0 || item.Version != expectedVersion {
		return LibraryItem{}, ErrConflict
	}
	_, runtime, err := s.library(ctx, item.SiteID, targetLibraryID)
	if err != nil {
		return LibraryItem{}, err
	}
	moved, err := s.repository.MoveLibraryItem(ctx, actor.AuditUserID(), id, targetLibraryID, expectedVersion, revisionPolicyFor(runtime).LibraryItems)
	if err != nil {
		return LibraryItem{}, fmt.Errorf("move library item: %w", err)
	}
	return cloneLibraryItem(moved), nil
}

func (s *LibraryService) Query(ctx context.Context, actor security.Actor, query LibraryItemQuery) (LibraryItemPage, error) {
	if err := s.common.authorizer.Check(ctx, actor, readPermission); err != nil {
		return LibraryItemPage{}, err
	}
	library, runtime, err := s.library(ctx, query.SiteID, query.LibraryID)
	if err != nil {
		return LibraryItemPage{}, err
	}
	if err := s.resolveLibraryQuery(ctx, library, runtime, &query); err != nil {
		return LibraryItemPage{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := query.Validate(); err != nil {
		return LibraryItemPage{}, ErrInvalid
	}
	page, err := s.repository.QueryLibraryItems(ctx, query)
	if err != nil {
		return LibraryItemPage{}, fmt.Errorf("query library items: %w", err)
	}
	for index := range page.Items {
		page.Items[index] = cloneLibraryItem(page.Items[index])
	}
	return page, nil
}

func (s *LibraryService) resolveLibraryQuery(ctx context.Context, library Resource, runtime *site.Runtime, query *LibraryItemQuery) error {
	custom := false
	for _, condition := range query.Filters {
		custom = custom || IsCustomFieldPath(condition.Field)
	}
	for _, item := range query.Sort {
		custom = custom || IsCustomFieldPath(item.Field)
	}

	codes := []template.Code(nil)
	if custom {
		var err error
		codes, err = s.repository.LibraryItemTemplateCodes(ctx, library.SiteID, library.ID)
		if err != nil {
			return err
		}
		if len(codes) == 0 {
			if code, ok := library.TypeSettings["default_item_template"].(string); ok && code != "" {
				codes = []template.Code{template.Code(code)}
			}
		}
	}

	metadata := make(map[FieldPath]field.StorageMetadata)
	resolve := func(path FieldPath) (field.StorageMetadata, error) {
		if resolved, exists := metadata[path]; exists {
			return resolved, nil
		}
		if !IsCustomFieldPath(path) {
			kind, err := BuiltinFieldStorageKind(path)
			if err == nil {
				metadata[path] = field.StorageMetadata{Kind: kind}
			}
			return field.StorageMetadata{Kind: kind}, err
		}
		key := strings.TrimPrefix(string(path), "resource.field.")
		var resolved field.StorageMetadata
		found := false
		for _, code := range codes {
			templateRuntime, exists := runtime.Profile().Template(code)
			if !exists {
				return field.StorageMetadata{}, fmt.Errorf("library item template %q is unavailable", code)
			}
			candidate, exists := templateRuntime.FieldSchema().Storage(key)
			if !exists {
				continue
			}
			if found && resolved.Kind != candidate.Kind {
				return field.StorageMetadata{}, fmt.Errorf(
					"field %q has incompatible storage kinds %q and %q",
					path, resolved.Kind, candidate.Kind,
				)
			}
			if found && resolved.Multiple != candidate.Multiple {
				return field.StorageMetadata{}, fmt.Errorf(
					"field %q has incompatible multiplicity %t and %t",
					path, resolved.Multiple, candidate.Multiple,
				)
			}
			resolved = candidate
			found = true
		}
		if !found {
			return field.StorageMetadata{}, fmt.Errorf("field %q is not defined by LibraryItem templates", path)
		}
		metadata[path] = resolved
		return resolved, nil
	}
	for index := range query.Filters {
		resolved, err := resolve(query.Filters[index].Field)
		if err != nil {
			return err
		}
		query.Filters[index].Kind = resolved.Kind
		query.Filters[index].Value, err = normalizeQueryFilterValue(resolved.Kind, query.Filters[index].Operator, query.Filters[index].Value)
		if err != nil {
			return fmt.Errorf("field %q: %w", query.Filters[index].Field, err)
		}
	}
	for index := range query.Sort {
		resolved, err := resolve(query.Sort[index].Field)
		if err != nil {
			return err
		}
		if IsCustomFieldPath(query.Sort[index].Field) && resolved.Multiple {
			return fmt.Errorf("field %q is multi-valued and cannot be sorted", query.Sort[index].Field)
		}
		query.Sort[index].Kind = resolved.Kind
	}
	return nil
}

func normalizeQueryFilterValue(kind field.StorageKind, operator FilterOperator, value any) (any, error) {
	if operator == FilterIn || operator == FilterNotIn {
		items, ok := value.([]any)
		if !ok {
			return nil, errors.New("set filter value is invalid")
		}
		result := make([]any, len(items))
		for index, item := range items {
			normalized, err := normalizeQueryFilterValue(kind, FilterEqual, item)
			if err != nil {
				return nil, err
			}
			result[index] = normalized
		}
		return result, nil
	}
	if kind == field.StorageTimestamp {
		if timestamp, ok := value.(time.Time); ok {
			return timestamp, nil
		}
		raw, ok := value.(string)
		if !ok {
			return nil, errors.New("timestamp filter value must be RFC3339")
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, errors.New("timestamp filter value must be RFC3339")
		}
		return parsed, nil
	}
	return value, nil
}

func (s *LibraryService) Delete(ctx context.Context, actor security.Actor, id ID, permanent bool) error {
	ctx, finishHooks, hookErr := s.common.beginMutation(ctx, actor, deleteOperation(permanent))
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	if err := s.common.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if permanent {
		return s.repository.DeleteLibraryItem(ctx, id)
	}
	return s.repository.SoftDeleteLibraryItem(ctx, actor.AuditUserID(), id)
}

func (s *LibraryService) Restore(ctx context.Context, actor security.Actor, id ID) error {
	ctx, finishHooks, hookErr := s.common.beginMutation(ctx, actor, OperationRestore)
	if hookErr != nil {
		return hookErr
	}
	defer finishHooks()

	if err := s.common.authorizer.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	return s.repository.RestoreLibraryItem(ctx, actor.AuditUserID(), id)
}

func (s *LibraryService) ResolvePublished(ctx context.Context, actor security.Actor, siteID site.ID, path string) (LibraryItem, Resource, error) {
	if err := s.common.authorizer.Check(ctx, actor, readPermission); err != nil {
		return LibraryItem{}, Resource{}, err
	}
	item, library, err := s.repository.ResolveLibraryItemRoute(ctx, siteID, path)
	if err != nil {
		return LibraryItem{}, Resource{}, err
	}
	now := time.Now().UTC()
	if library.DeletedAt != nil || item.DeletedAt != nil || !library.IsPublic || !item.IsPublic ||
		(library.PublishedAt != nil && now.Before(*library.PublishedAt)) || (library.UnpublishedAt != nil && !now.Before(*library.UnpublishedAt)) ||
		(item.PublishedAt != nil && now.Before(*item.PublishedAt)) || (item.UnpublishedAt != nil && !now.Before(*item.UnpublishedAt)) {
		return LibraryItem{}, Resource{}, ErrNotFound
	}
	return cloneLibraryItem(item), Clone(library), nil
}

func (s *LibraryService) library(ctx context.Context, siteID site.ID, id ID) (Resource, *site.Runtime, error) {
	item, err := s.common.repository.ByID(ctx, id)
	if err != nil {
		return Resource{}, nil, err
	}
	if item.SiteID != siteID || item.Type != resourcetype.Library || item.DeletedAt != nil {
		return Resource{}, nil, ErrInvalidReference
	}
	runtime, exists := s.common.runtime(ctx, siteID)
	if !exists {
		return Resource{}, nil, fmt.Errorf("resource site %d not found", siteID)
	}
	return item, runtime, nil
}

func (s *LibraryService) normalize(ctx context.Context, actor security.Actor, item LibraryItem, runtime *site.Runtime, trusted map[string]file.ID) (LibraryItem, error) {
	item.Title = strings.TrimSpace(item.Title)
	if item.Title == "" || !validSlug(item.Slug, pointerID(item.LibraryID)) {
		return LibraryItem{}, errors.New("library item title or slug is invalid")
	}
	if item.PublishedAt != nil && item.UnpublishedAt != nil && !item.UnpublishedAt.After(*item.PublishedAt) {
		return LibraryItem{}, errors.New("library item publication window is invalid")
	}
	if item.ImageMediaID != nil {
		if err := s.common.validateImageMedia(ctx, *item.ImageMediaID); err != nil {
			return LibraryItem{}, err
		}
	}
	if item.ContentType == nil {
		value := "html"
		item.ContentType = &value
	}
	if *item.ContentType != "html" {
		return LibraryItem{}, errors.New("library item content_type is unsupported")
	}
	if item.Template == nil {
		if len(item.Fields) != 0 {
			return LibraryItem{}, errors.New("library item without template has fields")
		}
		item.Fields, item.FieldValues, item.FileReferences = map[string]any{}, nil, nil
		return item, nil
	}
	templateRuntime, exists := runtime.Profile().Template(*item.Template)
	if !exists {
		return LibraryItem{}, fmt.Errorf("library item references unknown template %q", *item.Template)
	}
	fields, err := templateRuntime.FieldSchema().Validate(item.Fields)
	if err != nil {
		return LibraryItem{}, err
	}
	item.FieldValues, err = templateRuntime.FieldSchema().StoredValues(fields)
	if err != nil {
		return LibraryItem{}, err
	}
	if err := s.common.validateMediaFields(ctx, actor, item.FieldValues); err != nil {
		return LibraryItem{}, err
	}
	references, err := templateRuntime.FieldSchema().FileReferences(fields)
	if err != nil {
		return LibraryItem{}, err
	}
	if err := s.common.validateFileReferences(ctx, actor, references, trusted); err != nil {
		return LibraryItem{}, err
	}
	item.Fields, item.FileReferences = fields, resourceFileReferenceMap(references)
	return item, nil
}

func EffectiveLibraryItemURL(library Resource, item LibraryItem) (string, error) {
	if library.Path == nil {
		return "", errors.New("library has no path")
	}
	pattern, _ := library.TypeSettings["item_url_pattern"].(string)
	if pattern == "" {
		pattern = resourcetype.DefaultItemURLPattern
	}
	if err := resourcetype.ValidateItemURLPattern(pattern); err != nil {
		return "", err
	}
	date := item.CreatedAt.UTC()
	if item.PublishedAt != nil {
		date = item.PublishedAt.UTC()
	}
	replacements := map[string]string{
		"{id}": strconv.FormatInt(int64(item.ID), 10), "{slug}": item.Slug,
		"{year}": fmt.Sprintf("%04d", date.Year()), "{month}": fmt.Sprintf("%02d", date.Month()), "{day}": fmt.Sprintf("%02d", date.Day()),
	}
	result := pattern
	for token, value := range replacements {
		result = strings.ReplaceAll(result, token, value)
	}
	return strings.TrimRight(*library.Path, "/") + result, nil
}

type LibraryRouteKey struct {
	ID   ID
	Slug string
}

func MatchLibraryItemPattern(pattern, relativePath string) (LibraryRouteKey, bool) {
	if err := resourcetype.ValidateItemURLPattern(pattern); err != nil {
		return LibraryRouteKey{}, false
	}
	var expression strings.Builder
	expression.WriteByte('^')
	for cursor := 0; cursor < len(pattern); {
		open := strings.IndexByte(pattern[cursor:], '{')
		if open < 0 {
			expression.WriteString(regexp.QuoteMeta(pattern[cursor:]))
			break
		}
		open += cursor
		expression.WriteString(regexp.QuoteMeta(pattern[cursor:open]))
		close := strings.IndexByte(pattern[open:], '}') + open
		switch pattern[open+1 : close] {
		case "id":
			expression.WriteString(`(?P<id>[1-9][0-9]*)`)
		case "slug":
			expression.WriteString(`(?P<slug>[a-z0-9]+(?:-[a-z0-9]+)*)`)
		case "year":
			expression.WriteString(`[0-9]{4}`)
		case "month", "day":
			expression.WriteString(`[0-9]{2}`)
		}
		cursor = close + 1
	}
	expression.WriteByte('$')
	compiled := regexp.MustCompile(expression.String())
	match := compiled.FindStringSubmatch(relativePath)
	if match == nil {
		return LibraryRouteKey{}, false
	}
	result := LibraryRouteKey{}
	for index, name := range compiled.SubexpNames() {
		if index == 0 || name == "" {
			continue
		}
		if name == "slug" {
			result.Slug = match[index]
		}
		if name == "id" {
			parsed, _ := strconv.ParseInt(match[index], 10, 64)
			result.ID = ID(parsed)
		}
	}
	return result, true
}

type LibraryCursor struct {
	Values []any
	ID     ID
}

type libraryCursorPayload struct {
	Fingerprint string            `json:"fingerprint"`
	Values      []json.RawMessage `json:"values"`
	ID          ID                `json:"id"`
}

func (q LibraryItemQuery) Validate() error {
	if q.SiteID <= 0 || q.LibraryID <= 0 || q.Limit <= 0 || q.Limit > 100 {
		return ErrInvalid
	}
	for _, condition := range q.Filters {
		if !LibraryItemFilterField(condition.Field) || condition.Validate() != nil {
			return ErrInvalid
		}
	}
	idSeen := false
	for index, item := range q.Sort {
		if !LibraryItemSortField(item.Field) || item.Validate() != nil {
			return ErrInvalid
		}
		if item.Field == FieldID {
			if idSeen || index != len(q.Sort)-1 {
				return ErrInvalid
			}
			idSeen = true
		}
	}
	if q.Cursor != "" {
		if _, err := DecodeLibraryCursor(q); err != nil {
			return err
		}
	}
	return nil
}

func LibraryItemFilterField(path FieldPath) bool {
	if IsCustomFieldPath(path) {
		return true
	}
	switch path {
	case FieldID, FieldTitle, FieldSlug, FieldAnnotation, FieldTemplate,
		FieldIsPublic, FieldIsSearchable, FieldPublishedAt, FieldCreatedAt, FieldUpdatedAt:
		return true
	default:
		return false
	}
}

func LibraryItemSortField(path FieldPath) bool {
	if IsCustomFieldPath(path) {
		return true
	}
	switch path {
	case FieldID, FieldTitle, FieldSlug, FieldTemplate,
		FieldPublishedAt, FieldCreatedAt, FieldUpdatedAt:
		return true
	default:
		return false
	}
}

// LibraryItemSorts returns non-ID sort keys and the stable ID tie-breaker.
func LibraryItemSorts(query LibraryItemQuery) ([]Sort, SortDirection) {
	if len(query.Sort) == 0 {
		return nil, SortDescending
	}
	result := append([]Sort(nil), query.Sort...)
	idDirection := SortAscending
	if result[len(result)-1].Field == FieldID {
		idDirection = result[len(result)-1].Direction
		result = result[:len(result)-1]
	}
	return result, idDirection
}

func EncodeLibraryCursor(query LibraryItemQuery, item LibraryItem) (string, error) {
	if item.ID <= 0 {
		return "", ErrInvalid
	}
	sorts, _ := LibraryItemSorts(query)
	payload := libraryCursorPayload{Fingerprint: libraryQueryFingerprint(query), ID: item.ID}
	payload.Values = make([]json.RawMessage, len(sorts))
	for index, sort := range sorts {
		value := libraryItemSortValue(item, sort.Field)
		raw, err := json.Marshal(value)
		if err != nil {
			return "", fmt.Errorf("encode library cursor value: %w", err)
		}
		payload.Values[index] = raw
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode library cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func DecodeLibraryCursor(query LibraryItemQuery) (LibraryCursor, error) {
	if query.Cursor == "" {
		return LibraryCursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
	if err != nil {
		return LibraryCursor{}, ErrInvalid
	}
	var payload libraryCursorPayload
	if err := json.Unmarshal(raw, &payload); err != nil || payload.ID <= 0 || payload.Fingerprint != libraryQueryFingerprint(query) {
		return LibraryCursor{}, ErrInvalid
	}
	sorts, _ := LibraryItemSorts(query)
	if len(payload.Values) != len(sorts) {
		return LibraryCursor{}, ErrInvalid
	}
	result := LibraryCursor{Values: make([]any, len(sorts)), ID: payload.ID}
	for index, sort := range sorts {
		value, err := decodeCursorValue(payload.Values[index], sort)
		if err != nil {
			return LibraryCursor{}, ErrInvalid
		}
		result.Values[index] = value
	}
	return result, nil
}

func libraryQueryFingerprint(query LibraryItemQuery) string {
	query.Cursor = ""
	query.Limit = 0
	query.Filters = append([]FilterCondition(nil), query.Filters...)
	sort.SliceStable(query.Filters, func(i, j int) bool {
		left, _ := json.Marshal(query.Filters[i])
		right, _ := json.Marshal(query.Filters[j])
		return string(left) < string(right)
	})
	raw, _ := json.Marshal(query)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func libraryItemSortValue(item LibraryItem, path FieldPath) any {
	switch path {
	case FieldTitle:
		return item.Title
	case FieldSlug:
		return item.Slug
	case FieldTemplate:
		if item.Template == nil {
			return nil
		}
		return string(*item.Template)
	case FieldPublishedAt:
		return item.PublishedAt
	case FieldCreatedAt:
		return item.CreatedAt
	case FieldUpdatedAt:
		return item.UpdatedAt
	default:
		key := strings.TrimPrefix(string(path), "resource.field.")
		value := item.Fields[key]
		reflected := reflect.ValueOf(value)
		if reflected.IsValid() && reflected.Kind() == reflect.Slice {
			if reflected.Len() == 0 {
				return nil
			}
			return reflected.Index(0).Interface()
		}
		return value
	}
}

func decodeCursorValue(raw json.RawMessage, sort Sort) (any, error) {
	if string(raw) == "null" {
		return nil, nil
	}
	kind, err := sortStorageKind(sort)
	if err != nil {
		return nil, err
	}
	switch kind {
	case field.StorageString:
		var value string
		err = json.Unmarshal(raw, &value)
		return value, err
	case field.StorageInteger, field.StorageReference:
		var value int64
		err = json.Unmarshal(raw, &value)
		return value, err
	case field.StorageFloat:
		var value float64
		err = json.Unmarshal(raw, &value)
		return value, err
	case field.StorageTimestamp:
		var value time.Time
		err = json.Unmarshal(raw, &value)
		return value, err
	default:
		return nil, ErrInvalid
	}
}

func pointerID(id ID) *ID { return &id }

func cloneLibraryItem(item LibraryItem) LibraryItem {
	item.Template = cloneTemplateCode(item.Template)
	item.ContentType = cloneString(item.ContentType)
	item.ImageMediaID = cloneMediaID(item.ImageMediaID)
	item.PublishedAt = cloneTime(item.PublishedAt)
	item.UnpublishedAt = cloneTime(item.UnpublishedAt)
	item.Fields = cloneMap(item.Fields)
	item.FieldValues = cloneStoredValues(item.FieldValues)
	item.Widgets = widget.CloneBindings(item.Widgets)
	item.FileReferences = cloneFileReferences(item.FileReferences)
	item.CreatedBy = cloneUserID(item.CreatedBy)
	item.UpdatedBy = cloneUserID(item.UpdatedBy)
	item.DeletedAt = cloneTime(item.DeletedAt)
	item.DeletedBy = cloneUserID(item.DeletedBy)
	return item
}

func deleteOperation(permanent bool) Operation {
	if permanent {
		return OperationDelete
	}
	return OperationTrash
}
