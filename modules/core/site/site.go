package site

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	"golang.org/x/text/language"
)

type ID int64

var (
	ErrNotFound    = errors.New("site not found")
	ErrConflict    = errors.New("site conflict")
	ErrInvalid     = errors.New("invalid site")
	ErrUnavailable = errors.New("site runtime is stale or unavailable")

	readPermission = permission.MustCode(
		"core",
		"site",
		permission.Read,
	)
	updatePermission = permission.MustCode(
		"core",
		"site",
		permission.Update,
	)
	createPermission = permission.MustCode(
		"core",
		"site",
		permission.Create,
	)
	deletePermission = permission.MustCode(
		"core",
		"site",
		permission.Delete,
	)
)

type Site struct {
	Version        int64
	ID             ID
	ProfileCode    kernel.ProfileCode
	Domain         string
	Locale         string
	Settings       map[string]any
	IsPublic       bool
	CreatedAt      time.Time
	UpdatedAt      time.Time
	CreatedBy      *security.UserID
	UpdatedBy      *security.UserID
	FileReferences map[string]file.ID
}

type Repository interface {
	List(context.Context) ([]Site, error)
	Update(
		context.Context,
		*security.UserID,
		Site,
	) (Site, error)
}

type ManagementRepository interface {
	Repository
	FindByID(context.Context, ID) (Site, error)
	FindByDomain(context.Context, string) (Site, error)
	ListPage(context.Context, ListQuery) (Page, error)
	Create(context.Context, *security.UserID, Site) (Site, error)
	Delete(context.Context, ID) error
}

type StatisticsRepository interface {
	Statistics(context.Context, StatisticsQuery) (Statistics, error)
}

type Scope struct {
	All     bool
	SiteIDs []ID
}

type ListQuery struct {
	Search    string
	Page      int
	PerPage   int
	Scope     Scope
	ExcludeID *ID
}

type Page struct {
	Items []Site
	Total int
}

type StatisticsQuery struct {
	Scope Scope
	Limit int
}

type Statistics struct {
	Items   []Site
	Total   int
	Public  int
	Private int
}

type Access interface {
	security.Authorizer
	IsGuestSubject(context.Context, security.Actor) (bool, error)
}

type UpdateInput struct {
	ID          ID
	ProfileCode kernel.ProfileCode
	Domain      string
	Locale      string
	Settings    map[string]any
	IsPublic    bool
}

type CreateInput struct {
	ProfileCode kernel.ProfileCode
	Domain      string
	Locale      string
	Settings    map[string]any
	IsPublic    bool
}

type Runtime struct {
	site           Site
	profileRuntime *kernel.ProfileRuntime
	fileReferences []field.FileReference
}

func NewRuntimeFromBlueprint(
	ctx context.Context,
	item Site,
	blueprint *kernel.ProfileBlueprint,
) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("site runtime context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if blueprint == nil {
		return nil, errors.New("profile blueprint is nil")
	}
	item, fileReferences, err := normalizeRuntimeSite(
		item,
		blueprint.Profile().Code,
		blueprint.ParamSchema(),
	)
	if err != nil {
		return nil, err
	}
	profileRuntime, err := blueprint.Build(
		ctx,
		kernel.NewSiteRuntimeScope(
			fmt.Sprint(item.ID),
			item.ProfileCode,
			item.Domain,
			item.Locale,
			item.IsPublic,
			item.Settings,
		),
	)
	if err != nil {
		return nil, err
	}
	return &Runtime{
		site:           item,
		profileRuntime: profileRuntime,
		fileReferences: fileReferences,
	}, nil
}

func normalizeRuntimeSite(
	item Site,
	profileCode kernel.ProfileCode,
	paramSchema *field.Schema,
) (Site, []field.FileReference, error) {
	if item.ID <= 0 {
		return Site{}, nil, errors.New("invalid site id")
	}
	if item.ProfileCode == "" {
		return Site{}, nil, errors.New("site profile code is empty")
	}
	if item.ProfileCode != profileCode {
		return Site{}, nil, fmt.Errorf(
			"site profile %q does not match blueprint profile %q",
			item.ProfileCode,
			profileCode,
		)
	}
	domain, err := NormalizeDomain(item.Domain)
	if err != nil {
		return Site{}, nil, err
	}
	item.Domain = domain
	item.Locale = strings.TrimSpace(item.Locale)
	if item.Locale == "" {
		return Site{}, nil, errors.New("site locale is empty")
	}
	if _, err := language.Parse(item.Locale); err != nil {
		return Site{}, nil, fmt.Errorf("site locale %q is invalid: %w", item.Locale, err)
	}
	if paramSchema == nil {
		return Site{}, nil, errors.New("profile param schema is nil")
	}
	settings, err := paramSchema.Validate(item.Settings)
	if err != nil {
		return Site{}, nil, fmt.Errorf("validate site settings: %w", err)
	}
	item.Settings = cloneSettings(settings)
	fileReferences, err := paramSchema.FileReferences(settings)
	if err != nil {
		return Site{}, nil, fmt.Errorf("collect site file references: %w", err)
	}
	item.FileReferences = fileReferenceMap(fileReferences)
	return item, fileReferences, nil
}

func (r *Runtime) Site() Site {
	result := r.site
	result.Settings = cloneSettings(result.Settings)
	result.CreatedBy = cloneUserID(result.CreatedBy)
	result.UpdatedBy = cloneUserID(result.UpdatedBy)
	result.FileReferences = cloneFileReferences(result.FileReferences)
	return result
}

func (r *Runtime) Profile() *kernel.ProfileRuntime {
	return r.profileRuntime
}

type ProfileResolver interface {
	ProfileBlueprint(
		kernel.ProfileCode,
	) (*kernel.ProfileBlueprint, bool)
}

type runtimeSnapshot struct {
	byDomain map[string]*Runtime
	byID     map[ID]*Runtime
}

type RuntimePlan struct {
	current []*Runtime
	next    []*Runtime
}

func (p RuntimePlan) Current() []*Runtime {
	return append([]*Runtime(nil), p.current...)
}

func (p RuntimePlan) Next() []*Runtime {
	return append([]*Runtime(nil), p.next...)
}

type RuntimePreparation struct {
	Publish func()
	Abort   func()
}

// RuntimePreparer validates and builds detached transport state for a complete
// catalog transition. It must defer visible state changes to RuntimePreparation
// so a later preparer failure cannot partially publish the candidate runtimes.
type RuntimePreparer func(
	context.Context,
	RuntimePlan,
) (RuntimePreparation, error)

type Catalog struct {
	repository Repository
	profiles   ProfileResolver
	access     Access
	files      file.Service
	preparers  []RuntimePreparer

	snapshot   atomic.Pointer[runtimeSnapshot]
	mutationMu sync.Mutex
}

// AddRuntimePreparer prepares the currently published runtimes and every
// runtime built by later reload/create/update operations before publication.
func (c *Catalog) AddRuntimePreparer(
	ctx context.Context,
	preparer RuntimePreparer,
) error {
	if c == nil {
		return errors.New("site catalog is nil")
	}
	if ctx == nil {
		return errors.New("site runtime prepare context is nil")
	}
	if preparer == nil {
		return errors.New("site runtime preparer is nil")
	}
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	snapshot := c.snapshot.Load()
	if snapshot == nil {
		return errors.New("site runtime snapshot is nil")
	}
	runtimes := snapshotRuntimes(snapshot)
	preparation, err := preparer(ctx, RuntimePlan{
		current: runtimes,
		next:    runtimes,
	})
	if err != nil {
		return fmt.Errorf("prepare current site runtimes: %w", err)
	}
	applyRuntimePreparations([]RuntimePreparation{preparation})
	c.preparers = append(c.preparers, preparer)
	return nil
}

func (c *Catalog) prepareRuntimePlan(
	ctx context.Context,
	current *runtimeSnapshot,
	next *runtimeSnapshot,
) ([]RuntimePreparation, error) {
	plan := RuntimePlan{
		current: snapshotRuntimes(current),
		next:    snapshotRuntimes(next),
	}
	result, err := prepareRuntimeTransitions(ctx, plan)
	if err != nil {
		return nil, err
	}
	for _, preparer := range c.preparers {
		preparation, err := preparer(ctx, plan)
		if err != nil {
			abortRuntimePreparations(result)
			return nil, err
		}
		result = append(result, preparation)
	}
	return result, nil
}

func prepareRuntimeTransitions(
	ctx context.Context,
	plan RuntimePlan,
) ([]RuntimePreparation, error) {
	nextByID := make(map[ID]*Runtime, len(plan.next))
	for _, runtime := range plan.next {
		if runtime != nil {
			nextByID[runtime.site.ID] = runtime
		}
	}
	result := make([]RuntimePreparation, 0)
	for _, current := range plan.current {
		if current == nil || current.profileRuntime == nil {
			abortRuntimePreparations(result)
			return nil, errors.New("current site runtime is invalid")
		}
		next, exists := nextByID[current.site.ID]
		transition := kernel.RuntimeTransition{
			ScopeID:     fmt.Sprint(current.site.ID),
			FromProfile: current.site.ProfileCode,
		}
		switch {
		case !exists:
			transition.Reason = kernel.RuntimeTransitionSiteDelete
		case next.site.ProfileCode != current.site.ProfileCode:
			transition.Reason = kernel.RuntimeTransitionProfileChange
			transition.ToProfile = next.site.ProfileCode
		default:
			continue
		}
		for _, moduleRuntime := range current.profileRuntime.Modules() {
			participant, ok := moduleRuntime.(kernel.RuntimeTransitionParticipant)
			if !ok {
				continue
			}
			prepared, err := participant.PrepareRuntimeTransition(ctx, transition)
			if err != nil {
				abortRuntimePreparations(result)
				return nil, fmt.Errorf(
					"prepare module %q for %s: %w",
					moduleRuntime.ModuleCode(), transition.Reason, err,
				)
			}
			if prepared == nil {
				abortRuntimePreparations(result)
				return nil, fmt.Errorf(
					"module %q returned a nil prepared runtime transition",
					moduleRuntime.ModuleCode(),
				)
			}
			result = append(result, RuntimePreparation{
				Publish: prepared.Commit,
				Abort:   prepared.Abort,
			})
		}
	}
	return result, nil
}

func (c *Catalog) publishRuntimeSnapshot(
	next *runtimeSnapshot,
	preparations []RuntimePreparation,
) {
	c.snapshot.Store(next)
	applyRuntimePreparations(preparations)
}

func applyRuntimePreparations(preparations []RuntimePreparation) {
	for _, preparation := range preparations {
		if preparation.Publish != nil {
			preparation.Publish()
		}
	}
}

func abortRuntimePreparations(preparations []RuntimePreparation) {
	for index := len(preparations) - 1; index >= 0; index-- {
		if preparations[index].Abort != nil {
			preparations[index].Abort()
		}
	}
}

func NewCatalog(
	repository Repository,
	profiles ProfileResolver,
	access Access,
	fileServices ...file.Service,
) (*Catalog, error) {
	if repository == nil {
		return nil, errors.New("site repository is nil")
	}

	if profiles == nil {
		return nil, errors.New("profile resolver is nil")
	}
	if access == nil {
		return nil, errors.New("site access service is nil")
	}

	catalog := &Catalog{
		repository: repository,
		profiles:   profiles,
		access:     access,
	}
	if len(fileServices) > 0 {
		catalog.files = fileServices[0]
	}

	catalog.snapshot.Store(&runtimeSnapshot{
		byDomain: make(map[string]*Runtime),
		byID:     make(map[ID]*Runtime),
	})

	return catalog, nil
}

func (c *Catalog) RuntimeByDomain(
	domain string,
) (*Runtime, bool) {
	domain, err := NormalizeDomain(domain)
	if err != nil {
		return nil, false
	}

	snapshot := c.snapshot.Load()
	if snapshot == nil {
		return nil, false
	}

	runtime, exists := snapshot.byDomain[domain]
	return runtime, exists
}

func (c *Catalog) RuntimeByID(
	id ID,
) (*Runtime, bool) {
	if id <= 0 {
		return nil, false
	}

	snapshot := c.snapshot.Load()
	if snapshot == nil {
		return nil, false
	}

	runtime, exists := snapshot.byID[id]
	return runtime, exists
}

func (c *Catalog) Runtimes() []*Runtime {
	if c == nil {
		return nil
	}
	snapshot := c.snapshot.Load()
	if snapshot == nil {
		return nil
	}
	return snapshotRuntimes(snapshot)
}

func snapshotRuntimes(snapshot *runtimeSnapshot) []*Runtime {
	if snapshot == nil {
		return nil
	}
	ids := make([]ID, 0, len(snapshot.byID))
	for id := range snapshot.byID {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]*Runtime, 0, len(ids))
	for _, id := range ids {
		result = append(result, snapshot.byID[id])
	}
	return result
}

func (c *Catalog) ResolveByDomain(
	ctx context.Context,
	actor security.Actor,
	domain string,
) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("site resolve context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	runtime, exists := c.RuntimeByDomain(domain)
	if !exists {
		return nil, ErrNotFound
	}
	if err := c.CheckReadAccess(ctx, actor, runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

// CheckReadAccess applies catalog-owned site visibility rules to a runtime
// obtained from a prepared catalog plan, such as an HTTP transport snapshot.
func (c *Catalog) CheckReadAccess(
	ctx context.Context,
	actor security.Actor,
	runtime *Runtime,
) error {
	if ctx == nil {
		return errors.New("site access context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if runtime == nil {
		return ErrNotFound
	}
	if err := c.CheckCurrent(ctx, runtime); err != nil {
		return err
	}
	if err := c.access.Check(ctx, actor, readPermission); err != nil {
		return err
	}
	guest, err := c.access.IsGuestSubject(ctx, actor)
	if err != nil {
		return err
	}
	if guest && !runtime.site.IsPublic {
		return security.ErrForbidden
	}
	return nil
}

func (c *Catalog) Reload(ctx context.Context) error {
	if ctx == nil {
		return errors.New("site reload context is nil")
	}

	if err := ctx.Err(); err != nil {
		return err
	}

	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()

	sites, err := c.repository.List(ctx)
	if err != nil {
		return fmt.Errorf("list sites: %w", err)
	}

	next := &runtimeSnapshot{
		byDomain: make(map[string]*Runtime, len(sites)),
		byID:     make(map[ID]*Runtime, len(sites)),
	}

	for index, item := range sites {
		blueprint, exists := c.profiles.ProfileBlueprint(
			item.ProfileCode,
		)
		if !exists {
			return fmt.Errorf(
				"site at index %d references unknown profile %q",
				index,
				item.ProfileCode,
			)
		}

		runtime, err := NewRuntimeFromBlueprint(ctx, item, blueprint)
		if err != nil {
			return fmt.Errorf(
				"build site runtime at index %d with id %d: %w",
				index,
				item.ID,
				err,
			)
		}
		if err := c.validateFileReferences(ctx, security.System(), runtime.fileReferences, nil); err != nil {
			return fmt.Errorf("validate site file references at index %d: %w", index, err)
		}
		domain := runtime.site.Domain

		if _, exists := next.byDomain[domain]; exists {
			return fmt.Errorf(
				"duplicate normalized site domain %q",
				domain,
			)
		}
		if _, exists := next.byID[item.ID]; exists {
			return fmt.Errorf(
				"duplicate site id %d",
				item.ID,
			)
		}

		next.byDomain[domain] = runtime
		next.byID[item.ID] = runtime
	}

	current := c.snapshot.Load()
	preparations, err := c.prepareRuntimePlan(ctx, current, next)
	if err != nil {
		return fmt.Errorf("prepare reloaded site runtimes: %w", err)
	}
	c.publishRuntimeSnapshot(next, preparations)
	return nil
}

func (c *Catalog) Create(
	ctx context.Context,
	actor security.Actor,
	input CreateInput,
) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("site create context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.access.Check(ctx, actor, createPermission); err != nil {
		return nil, err
	}
	blueprint, exists := c.profiles.ProfileBlueprint(input.ProfileCode)
	if !exists {
		return nil, fmt.Errorf("%w: profile %q is unknown", ErrInvalid, input.ProfileCode)
	}

	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	currentSnapshot := c.snapshot.Load()
	if currentSnapshot == nil {
		return nil, errors.New("site runtime snapshot is nil")
	}

	candidate, fileReferences, err := normalizeRuntimeSite(Site{
		ID:          1,
		ProfileCode: input.ProfileCode,
		Domain:      input.Domain,
		Locale:      input.Locale,
		Settings:    cloneSettings(input.Settings),
		IsPublic:    input.IsPublic,
	}, blueprint.Profile().Code, blueprint.ParamSchema())
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := c.validateFileReferences(ctx, actor, fileReferences, nil); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if _, exists := currentSnapshot.byDomain[candidate.Domain]; exists {
		return nil, ErrConflict
	}
	management, ok := c.repository.(ManagementRepository)
	if !ok {
		return nil, errors.New("site management repository is unavailable")
	}
	candidate.ID = 0
	stored, err := management.Create(
		ctx,
		actor.AuditUserID(),
		candidate,
	)
	if err != nil {
		return nil, fmt.Errorf("create site: %w", err)
	}
	nextRuntime, err := NewRuntimeFromBlueprint(ctx, stored, blueprint)
	if err != nil {
		return nil, rollbackCreatedSite(
			ctx,
			management,
			stored.ID,
			fmt.Errorf("build created site runtime: %w", err),
		)
	}
	if _, exists := currentSnapshot.byID[nextRuntime.site.ID]; exists {
		return nil, rollbackCreatedSite(
			ctx,
			management,
			stored.ID,
			fmt.Errorf("%w: site id %d already exists", ErrConflict, stored.ID),
		)
	}
	if _, exists := currentSnapshot.byDomain[nextRuntime.site.Domain]; exists {
		return nil, rollbackCreatedSite(
			ctx,
			management,
			stored.ID,
			fmt.Errorf(
				"%w: site domain %q already exists",
				ErrConflict,
				nextRuntime.site.Domain,
			),
		)
	}
	nextSnapshot := cloneSnapshot(currentSnapshot, 1)
	nextSnapshot.byDomain[nextRuntime.site.Domain] = nextRuntime
	nextSnapshot.byID[nextRuntime.site.ID] = nextRuntime
	preparations, err := c.prepareRuntimePlan(ctx, currentSnapshot, nextSnapshot)
	if err != nil {
		return nil, rollbackCreatedSite(
			ctx,
			management,
			stored.ID,
			fmt.Errorf("prepare created site runtime: %w", err),
		)
	}
	c.publishRuntimeSnapshot(nextSnapshot, preparations)
	return nextRuntime, nil
}

func rollbackCreatedSite(
	ctx context.Context,
	repository ManagementRepository,
	id ID,
	cause error,
) error {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := repository.Delete(rollbackCtx, id); err != nil {
		return errors.Join(
			cause,
			fmt.Errorf("rollback created site %d: %w", id, err),
		)
	}
	return cause
}

func (c *Catalog) Update(
	ctx context.Context,
	actor security.Actor,
	input UpdateInput,
) (*Runtime, error) {
	if ctx == nil {
		return nil, errors.New("site settings update context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := c.access.Check(ctx, actor, updatePermission); err != nil {
		return nil, err
	}
	if input.ID <= 0 {
		return nil, fmt.Errorf("%w: invalid site id", ErrInvalid)
	}

	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()

	currentSnapshot := c.snapshot.Load()
	if currentSnapshot == nil {
		return nil, errors.New("site runtime snapshot is nil")
	}

	current, exists := currentSnapshot.byID[input.ID]
	if !exists {
		return nil, ErrNotFound
	}

	blueprint, exists := c.profiles.ProfileBlueprint(input.ProfileCode)
	if !exists {
		return nil, fmt.Errorf("%w: profile %q is unknown", ErrInvalid, input.ProfileCode)
	}
	item := current.Site()
	item.ProfileCode = input.ProfileCode
	item.Domain = input.Domain
	item.Locale = strings.TrimSpace(input.Locale)
	item.Settings = cloneSettings(input.Settings)
	item.IsPublic = input.IsPublic

	nextRuntime, err := NewRuntimeFromBlueprint(ctx, item, blueprint)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if err := c.validateFileReferences(ctx, actor, nextRuntime.fileReferences, current.site.FileReferences); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalid, err)
	}
	if existing, exists := currentSnapshot.byDomain[nextRuntime.site.Domain]; exists && existing.site.ID != input.ID {
		return nil, ErrConflict
	}
	nextSnapshot := cloneSnapshot(currentSnapshot, 0)
	delete(nextSnapshot.byDomain, current.site.Domain)
	nextSnapshot.byDomain[nextRuntime.site.Domain] = nextRuntime
	nextSnapshot.byID[input.ID] = nextRuntime
	preparations, err := c.prepareRuntimePlan(ctx, currentSnapshot, nextSnapshot)
	if err != nil {
		return nil, runtimePreparationError("prepare updated site runtime", err)
	}
	stored, err := c.repository.Update(
		ctx,
		actor.AuditUserID(),
		nextRuntime.Site(),
	)
	if err != nil {
		abortRuntimePreparations(preparations)
		return nil, fmt.Errorf("update site: %w", err)
	}
	nextRuntime.site.Version = stored.Version
	nextRuntime.site.CreatedAt = stored.CreatedAt
	nextRuntime.site.UpdatedAt = stored.UpdatedAt
	nextRuntime.site.CreatedBy = cloneUserID(stored.CreatedBy)
	nextRuntime.site.UpdatedBy = cloneUserID(stored.UpdatedBy)

	c.publishRuntimeSnapshot(nextSnapshot, preparations)

	return nextRuntime, nil
}

func (c *Catalog) Delete(
	ctx context.Context,
	actor security.Actor,
	id ID,
) error {
	if ctx == nil {
		return errors.New("site delete context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.access.Check(ctx, actor, deletePermission); err != nil {
		return err
	}
	if id <= 0 {
		return errors.New("invalid site id")
	}
	c.mutationMu.Lock()
	defer c.mutationMu.Unlock()
	currentSnapshot := c.snapshot.Load()
	if currentSnapshot == nil {
		return errors.New("site runtime snapshot is nil")
	}
	current, exists := currentSnapshot.byID[id]
	if !exists {
		return ErrNotFound
	}
	management, ok := c.repository.(ManagementRepository)
	if !ok {
		return errors.New("site management repository is unavailable")
	}
	nextSnapshot := cloneSnapshot(currentSnapshot, -1)
	delete(nextSnapshot.byDomain, current.site.Domain)
	delete(nextSnapshot.byID, id)
	preparations, err := c.prepareRuntimePlan(ctx, currentSnapshot, nextSnapshot)
	if err != nil {
		return runtimePreparationError("prepare deleted site runtime", err)
	}
	if err := management.Delete(ctx, id); err != nil {
		abortRuntimePreparations(preparations)
		return fmt.Errorf("delete site: %w", err)
	}
	c.publishRuntimeSnapshot(nextSnapshot, preparations)
	return nil
}

func runtimePreparationError(operation string, err error) error {
	if errors.Is(err, kernel.ErrRuntimeTransitionBlocked) {
		return fmt.Errorf("%w: %s: %w", ErrConflict, operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func cloneSnapshot(current *runtimeSnapshot, delta int) *runtimeSnapshot {
	capacity := len(current.byID) + delta
	if capacity < 0 {
		capacity = 0
	}
	next := &runtimeSnapshot{
		byDomain: make(map[string]*Runtime, capacity),
		byID:     make(map[ID]*Runtime, capacity),
	}
	for domain, runtime := range current.byDomain {
		next.byDomain[domain] = runtime
	}
	for id, runtime := range current.byID {
		next.byID[id] = runtime
	}
	return next
}

func NormalizeDomain(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("site domain is empty")
	}

	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}

	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	value = strings.TrimRight(value, ".")
	value = strings.ToLower(strings.TrimSpace(value))

	if value == "" {
		return "", errors.New("site domain is empty")
	}

	if net.ParseIP(value) == nil &&
		strings.ContainsAny(value, " /\\@:#") {
		return "", fmt.Errorf(
			"invalid site domain %q",
			value,
		)
	}

	return value, nil
}

func cloneSettings(source map[string]any) map[string]any {
	if source == nil {
		return map[string]any{}
	}

	result := make(map[string]any, len(source))

	for key, value := range source {
		result[key] = cloneSettingValue(value)
	}

	return result
}

func (c *Catalog) validateFileReferences(
	ctx context.Context,
	actor security.Actor,
	references []field.FileReference,
	trusted map[string]file.ID,
) error {
	if len(references) == 0 {
		return nil
	}
	if c.files == nil {
		return errors.New("site file service is unavailable")
	}
	for _, reference := range references {
		if trusted[reference.Key] == file.ID(reference.ID) {
			continue
		}
		item, err := c.files.GetFile(ctx, actor, file.ID(reference.ID))
		if err != nil {
			return fmt.Errorf("file field %q: %w", reference.Key, err)
		}
		if !field.FileMatches(reference.Options, item.Storage, item.MIMEType) {
			return fmt.Errorf("file field %q rejects selected file", reference.Key)
		}
	}
	return nil
}

func fileReferenceMap(references []field.FileReference) map[string]file.ID {
	if len(references) == 0 {
		return nil
	}
	result := make(map[string]file.ID, len(references))
	for _, reference := range references {
		result[reference.Key] = file.ID(reference.ID)
	}
	return result
}

func cloneFileReferences(source map[string]file.ID) map[string]file.ID {
	if source == nil {
		return nil
	}
	result := make(map[string]file.ID, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func cloneUserID(value *security.UserID) *security.UserID {
	if value == nil {
		return nil
	}
	result := *value
	return &result
}

func cloneSettingValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneSettings(typed)

	case []any:
		result := make([]any, len(typed))

		for index, item := range typed {
			result[index] = cloneSettingValue(item)
		}

		return result

	case []string:
		return append([]string(nil), typed...)

	default:
		return typed
	}
}
