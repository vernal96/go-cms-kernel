package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/domainevent"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type hookTestResolver struct{ kernel.DatabaseResolver }
type hookTestBus struct{ eventbus.Bus }
type hookTestMedia struct{ media.Service }
type hookTestAccess struct{ access.Service }
type hookRestrictedAccess struct{ hookTestAccess }

func (hookRestrictedAccess) IsPrivileged(context.Context, security.Actor) (bool, error) {
	return false, nil
}

func (hookTestAccess) Check(context.Context, security.Actor, permission.Code) error { return nil }
func (hookTestAccess) IsPrivileged(context.Context, security.Actor) (bool, error)   { return true, nil }

type hookTestSites map[site.ID]*site.Runtime

func (s hookTestSites) RuntimeByID(id site.ID) (*site.Runtime, bool) {
	runtime, ok := s[id]
	return runtime, ok
}

type hookTestModule struct {
	before func(context.Context, *resource.Change) error
	after  func(context.Context, entityhooks.Delivery, resource.EventPayload) error
}

func (hookTestModule) Code() kernel.ModuleCode { return "core" }
func (hookTestModule) Registry() kernel.ModuleRegistry {
	return kernel.ModuleRegistry{FieldTypes: field.StandardTypes(), ResourceTypes: resourcetype.StandardTypes()}
}
func (m hookTestModule) Build(_ context.Context, ctx kernel.ModuleContext) (kernel.ModuleRuntime, error) {
	for _, key := range []entityhooks.Key[resource.Change]{resource.BeforeCreate, resource.BeforeUpdate} {
		if err := entityhooks.RegisterBefore(ctx.EntityHooks(), key, key.Name(), m.before); err != nil {
			return nil, err
		}
	}
	for _, key := range []entityhooks.Key[resource.EventPayload]{resource.AfterCreate, resource.AfterUpdate, resource.AfterDelete} {
		if err := entityhooks.RegisterAfter(ctx.EntityHooks(), key, key.Name(), m.after); err != nil {
			return nil, err
		}
	}
	return hookTestRuntime{}, nil
}

type hookTestRuntime struct{}

func (hookTestRuntime) Widgets() []widget.Widget {
	return []widget.Widget{widget.Functional{
		Description: widget.Definition{Reference: widget.NewRef("hook_widget"), Label: "Hook widget", Description: "Entity hook integration widget", Fields: []field.Definition{{Key: "text", Label: "Text", Type: field.TypeString}}},
		Render: func(_ context.Context, _ widget.RenderInput, params map[string]any) (map[string]any, error) {
			return params, nil
		},
	}}
}
func (hookTestRuntime) ResourceRevisionPolicy() resource.RevisionPolicy {
	return resource.RevisionPolicy{LibraryItems: true}
}

func (hookTestRuntime) ModuleCode() kernel.ModuleCode { return "core" }

func TestPostgresEntityHooksResources(t *testing.T) {
	connector, database, ctx := openOutboxIntegrationDatabase(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	vetoID := resource.ID(0)
	var seen []resource.Operation
	var delivered []resource.EventPayload
	module := hookTestModule{before: func(_ context.Context, change *resource.Change) error {
		seen = append(seen, change.Operation)
		if change.Operation == resource.OperationWidgetCreate || change.Operation == resource.OperationWidgetUpdate {
			for i := range change.Data.Widgets {
				change.Data.Widgets[i].Params["text"] = "hook widget text"
			}
		}
		if change.Before != nil && change.Before.ID == vetoID {
			return errors.New("veto child")
		}
		if change.Data.Title == "invalid-after-hook" {
			change.Data.Title = ""
		} else if change.Operation == resource.OperationCreate {
			change.Data.Title = fmt.Sprintf("site-%d:%s", change.Candidate.SiteID, change.Data.Title)
		}
		return nil
	}, after: func(_ context.Context, d entityhooks.Delivery, p resource.EventPayload) error {
		if d.Target.ScopeID != fmt.Sprint(p.SiteID) && (p.Before == nil || d.Target.ScopeID != fmt.Sprint(p.Before.SiteID)) {
			return errors.New("wrong site delivery")
		}
		delivered = append(delivered, p)
		return nil
	}}
	factory, err := kernel.NewProfileRuntimeFactory(hookTestResolver{}, kernel.RuntimeServices{EventBus: hookTestBus{}, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "hooks", Modules: []kernel.ProfileModule{{Module: module}}, Templates: []template.Definition{{Code: "hook_template", Label: "Hook template", Layout: template.Layout{Body: []template.Item{template.ResourceWidgets{}}, Sidebar: []template.Item{template.ResourceWidgets{}}}, Fields: []field.Definition{{Key: "headline", Label: "Headline", Type: field.TypeString}}}}})
	if err != nil {
		t.Fatal(err)
	}
	sites := hookTestSites{}
	for i := 0; i < 2; i++ {
		stored, err := database.Sites().(site.ManagementRepository).Create(ctx, nil, site.Site{ProfileCode: "hooks", Domain: fmt.Sprintf("hooks-%d-%d.test", time.Now().UnixNano(), i), Locale: "en-US", Settings: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		runtime, err := site.NewRuntimeFromBlueprint(ctx, stored, blueprint)
		if err != nil {
			t.Fatal(err)
		}
		sites[stored.ID] = runtime
	}
	var siteIDs []site.ID
	for id := range sites {
		siteIDs = append(siteIDs, id)
	}
	service, err := resource.NewService(database.Resources(), sites, hookTestMedia{}, hookTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	actor := security.System()
	root, err := service.Create(ctx, actor, resource.CreateInput{SiteID: siteIDs[0], Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if root.Title != fmt.Sprintf("site-%d:root", root.SiteID) {
		t.Fatalf("hook title=%q", root.Title)
	}
	child, err := service.Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "child", Slug: "child"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := service.Create(ctx, actor, resource.CreateInput{SiteID: siteIDs[1], Title: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if other.Title != fmt.Sprintf("site-%d:other", other.SiteID) {
		t.Fatalf("site leakage: %q", other.Title)
	}
	var beforeCalls int
	if err := connector.Pool().QueryRow(ctx, `SELECT count(*) FROM core.entity_hook_calls`).Scan(&beforeCalls); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "invalid-after-hook", Slug: "invalid"}); err == nil {
		t.Fatal("invalid hook candidate saved")
	}

	// Simulate failure at the final outbox insertion after the entity and
	// recipient rows were inserted. All three must roll back together.
	if _, err := connector.Pool().Exec(ctx, `CREATE FUNCTION core.reject_hook_test_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'outbox unavailable'; END $$; CREATE TRIGGER reject_hook_test_event BEFORE INSERT ON core.outbox_messages FOR EACH ROW EXECUTE FUNCTION core.reject_hook_test_event();`); err != nil {
		t.Fatal(err)
	}
	_, writeErr := service.Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Title: "outbox failure", Slug: "outbox-failure"})
	if _, err := connector.Pool().Exec(ctx, `DROP TRIGGER reject_hook_test_event ON core.outbox_messages; DROP FUNCTION core.reject_hook_test_event();`); err != nil {
		t.Fatal(err)
	}
	if writeErr == nil {
		t.Fatal("outbox failure did not reject mutation")
	}
	var leaked bool
	if err := connector.Pool().QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM core.resources WHERE site_id=$1 AND slug='outbox-failure')`, root.SiteID).Scan(&leaked); err != nil {
		t.Fatal(err)
	}
	if leaked {
		t.Fatal("entity committed without its outbox event")
	}
	vetoID = child.ID
	if err := service.Delete(ctx, actor, root.ID); err == nil {
		t.Fatal("cascade veto ignored")
	}
	var afterCalls int
	var deleted bool
	if err := connector.Pool().QueryRow(ctx, `SELECT count(*) FROM core.entity_hook_calls`).Scan(&afterCalls); err != nil {
		t.Fatal(err)
	}
	if err := connector.Pool().QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM core.resources WHERE id=$1`, root.ID).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if afterCalls != beforeCalls || deleted {
		t.Fatal("failed mutation leaked state or deliveries")
	}
	vetoID = 0
	if err := service.Delete(ctx, actor, root.ID); err != nil {
		t.Fatal(err)
	}
	if err := service.Restore(ctx, actor, root.ID, true); err != nil {
		t.Fatal(err)
	}
	current, err := service.Get(ctx, actor, child.ID)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := service.Move(ctx, actor, child.ID, nil, 1, current.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.TransferToSite(ctx, actor, moved.ID, other.SiteID, moved.Version, nil); err != nil {
		t.Fatal(err)
	}
	if err := service.DeletePermanent(ctx, actor, moved.ID); err != nil {
		t.Fatal(err)
	}
	// Library items use the same resource extension points and transaction path.
	library, err := service.Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Type: resourcetype.Library, Title: "books", Slug: "books"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := resource.NewLibraryService(database.Resources().(resource.LibraryItemRepository), service)
	if err != nil {
		t.Fatal(err)
	}
	item, err := items.Create(ctx, actor, resource.CreateLibraryItemInput{SiteID: root.SiteID, LibraryID: library.ID, Title: "entry", Slug: "entry"})
	if err != nil {
		t.Fatal(err)
	}
	initialItemVersion := item.Version
	item, err = items.Update(ctx, actor, resource.UpdateLibraryItemInput{ID: item.ID, ExpectedVersion: item.Version, Title: "edited entry", Slug: "entry"})
	if err != nil {
		t.Fatal(err)
	}
	revisionsForItems, err := resource.NewRevisionService(database.Resources().(resource.RevisionRepository), service, items, hookTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	restoredItem, err := revisionsForItems.Restore(ctx, actor, item.SiteID, item.ID, initialItemVersion, item.Version)
	if err != nil {
		t.Fatal(err)
	}
	if restoredItem.Title != fmt.Sprintf("site-%d:entry", root.SiteID) {
		t.Fatal("library history snapshot lost")
	}
	secondLibrary, err := service.Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Type: resourcetype.Library, Title: "other books", Slug: "other-books"})
	if err != nil {
		t.Fatal(err)
	}
	item, err = items.Move(ctx, actor, item.ID, secondLibrary.ID, restoredItem.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := items.Delete(ctx, actor, item.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := items.Restore(ctx, actor, item.ID); err != nil {
		t.Fatal(err)
	}
	if err := items.Delete(ctx, actor, item.ID, true); err != nil {
		t.Fatal(err)
	}

	// Ordinary edits, dynamic fields, all widget operations, and history restore.
	templateCode := template.Code("hook_template")
	editable, err := service.Create(ctx, actor, resource.CreateInput{SiteID: root.SiteID, ParentID: &root.ID, Template: &templateCode, Title: "editable", Slug: "editable", Fields: map[string]any{"headline": "one"}})
	if err != nil {
		t.Fatal(err)
	}
	editable, err = service.Update(ctx, actor, resource.UpdateInput{ID: editable.ID, ExpectedVersion: editable.Version, ParentID: &root.ID, Type: resourcetype.Page, Template: &templateCode, Title: "updated", Slug: "editable", Fields: map[string]any{"headline": "two"}})
	if err != nil {
		t.Fatal(err)
	}
	if editable.Fields["headline"] != "two" {
		t.Fatal("field mutation lost")
	}
	binding, err := service.CreateWidget(ctx, actor, editable.ID, resource.CreateWidgetInput{ExpectedVersion: editable.Version, Code: "core_hook_widget", Area: widget.AreaBody, Columns: 12, Params: map[string]any{"text": "input"}})
	if err != nil {
		t.Fatal(err)
	}
	if binding.Params["text"] != "hook widget text" {
		t.Fatalf("widget hook result=%v", binding.Params)
	}
	editable, err = service.Get(ctx, actor, editable.ID)
	if err != nil {
		t.Fatal(err)
	}
	historicalVersion := editable.Version
	binding, err = service.UpdateWidget(ctx, actor, editable.ID, binding.ID, resource.UpdateWidgetInput{ExpectedVersion: editable.Version, Columns: 6, Params: map[string]any{"text": "update"}})
	if err != nil {
		t.Fatal(err)
	}
	editable, err = service.Get(ctx, actor, editable.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReorderWidgets(ctx, actor, editable.ID, editable.Version, []widget.Order{{ID: binding.ID, Area: widget.AreaSidebar, Position: 0}}); err != nil {
		t.Fatal(err)
	}
	editable, err = service.Get(ctx, actor, editable.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteWidget(ctx, actor, editable.ID, binding.ID, editable.Version); err != nil {
		t.Fatal(err)
	}
	editable, err = service.Get(ctx, actor, editable.ID)
	if err != nil {
		t.Fatal(err)
	}
	revisions, err := resource.NewRevisionService(database.Resources().(resource.RevisionRepository), service, items, hookTestAccess{})
	if err != nil {
		t.Fatal(err)
	}
	editable, err = revisions.Restore(ctx, actor, editable.SiteID, editable.ID, historicalVersion, editable.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(editable.Widgets) != 1 || editable.Widgets[0].Params["text"] != "hook widget text" {
		t.Fatal("revision widgets lost")
	}
	if err := entityhooks.PendingForScope(ctx, database.EntityHookSources(), entityhooks.Site, fmt.Sprint(root.SiteID)); !errors.Is(err, entityhooks.ErrBusy) {
		t.Fatalf("pending transition=%v", err)
	}
	runner, err := entityhooks.NewRunner(database.EntityHookSources(), func(target entityhooks.Target) (*entityhooks.Registry, bool) {
		for id, runtime := range sites {
			if fmt.Sprint(id) == target.ScopeID {
				return runtime.Profile().EntityHooks(), true
			}
		}
		return nil, false
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.ValidatePending(ctx); err != nil {
		t.Fatal(err)
	}
	messages := hookMessages(t, ctx, connector.Pool())
	for _, message := range messages {
		if err := runner.Handle(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	count := len(delivered)
	for _, message := range messages {
		if err := runner.Handle(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	if len(delivered) != count {
		t.Fatal("completed handlers repeated")
	}
	var deletedSnapshots int
	for _, payload := range delivered {
		if payload.After == nil {
			deletedSnapshots++
			if payload.Before == nil || payload.Before.Data.Title == "" {
				t.Fatal("deleted snapshot missing")
			}
		}
	}
	if deletedSnapshots != 2 {
		t.Fatalf("delete snapshots=%d", deletedSnapshots)
	}
	if err := entityhooks.PendingForScope(ctx, database.EntityHookSources(), entityhooks.Site, fmt.Sprint(root.SiteID)); err != nil {
		t.Fatal(err)
	}
	if len(seen) < 10 {
		t.Fatalf("specialized operations were not observed: %v", seen)
	}
}

type hookHasher struct{}

func (hookHasher) DummyHash() string { return "test-hash:dummy-password" }

func (hookHasher) Hash(password string) (string, error) { return "test-hash:" + password, nil }
func (hookHasher) Verify(password, hash string) (bool, bool, error) {
	return hash == "test-hash:"+password, false, nil
}

func TestPostgresEntityHooksUsersAndDeliveryRetry(t *testing.T) {
	connector, database, ctx := openOutboxIntegrationDatabase(t)
	registry := entityhooks.NewRegistry(entityhooks.Application, "")
	registrar := registry.ForModule("core", nil)
	beforeCount := 0
	before := func(_ context.Context, c *user.Change) error {
		beforeCount++
		if c.Operation == user.OperationCreate {
			c.Data.Name = "changed by hook"
			c.Password = "changed-secret-12345"
		}
		return nil
	}
	for _, key := range []entityhooks.Key[user.Change]{user.BeforeCreate, user.BeforeUpdate} {
		if err := entityhooks.RegisterBefore(registrar, key, key.Name(), before); err != nil {
			t.Fatal(err)
		}
	}
	successCalls, retryCalls := 0, 0
	if err := entityhooks.RegisterAfter(registrar, user.AfterCreate, "a.success", func(context.Context, entityhooks.Delivery, user.EventPayload) error { successCalls++; return nil }); err != nil {
		t.Fatal(err)
	}
	var idempotencyKey string
	if err := entityhooks.RegisterAfter(registrar, user.AfterCreate, "b.retry", func(_ context.Context, d entityhooks.Delivery, p user.EventPayload) error {
		retryCalls++
		if idempotencyKey == "" {
			idempotencyKey = d.IdempotencyKey()
		} else if idempotencyKey != d.IdempotencyKey() {
			t.Fatal("unstable idempotency key")
		}
		if retryCalls == 1 {
			return errors.New("temporary")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := entityhooks.RegisterAfter(registrar, user.AfterUpdate, "updated", func(context.Context, entityhooks.Delivery, user.EventPayload) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	groups, err := group.NewService(database.Groups(), hookTestAccess{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewService(database.Users(), hookHasher{}, hookTestMedia{}, groups, hookTestAccess{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	login := fmt.Sprintf("hooks%d", time.Now().UnixNano())
	created, err := users.Create(ctx, security.System(), user.CreateInput{Login: login, Email: login + "@example.test", Name: "original", Password: "original-secret-12345"})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "changed by hook" {
		t.Fatalf("name=%q", created.Name)
	}
	record, err := database.Users().ByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.PasswordHash != "test-hash:changed-secret-12345" {
		t.Fatal("password hook not applied")
	}
	if _, err := users.ChangePassword(ctx, security.System(), created.ID, "another-secret-12345"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Block(ctx, security.System(), created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := users.Unblock(ctx, security.System(), created.ID); err != nil {
		t.Fatal(err)
	}
	g, err := groups.Create(ctx, security.System(), group.CreateInput{Code: "hook_group_" + login, Name: "Hook group"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := groups.AddUser(ctx, security.System(), g.ID, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := groups.RemoveUser(ctx, security.System(), g.ID, created.ID); err != nil {
		t.Fatal(err)
	}

	if _, err := users.Update(ctx, security.System(), user.UpdateInput{ID: created.ID, Login: created.Login, Email: created.Email, Name: "admin edit"}); err != nil {
		t.Fatal(err)
	}
	actorUser := security.User(created.ID)
	if _, err := users.UpdateCurrent(ctx, actorUser, user.UpdateCurrentInput{Name: "self edit"}); err != nil {
		t.Fatal(err)
	}
	if _, err := users.UpdateCurrentPreferences(ctx, actorUser, user.Preferences{ColorScheme: user.ColorSchemeDark, AccentColor: user.AccentColorBlue}); err != nil {
		t.Fatal(err)
	}
	if _, err := users.UpdateCurrentAvatar(ctx, actorUser, nil); err != nil {
		t.Fatal(err)
	}
	if err := groups.ReplaceUserGroups(ctx, security.System(), created.ID, []group.ID{g.ID}); err != nil {
		t.Fatal(err)
	}
	if err := groups.Delete(ctx, security.System(), g.ID); err != nil {
		t.Fatal(err)
	}
	if beforeCount != 12 {
		t.Fatalf("before calls=%d", beforeCount)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	resolve := func(target entityhooks.Target) (*entityhooks.Registry, bool) {
		return registry, target.Scope == entityhooks.Application
	}
	runner, err := entityhooks.NewRunner(database.EntityHookSources(), resolve, logger)
	if err != nil {
		t.Fatal(err)
	}
	var messages []eventbus.Message
	for _, message := range hookMessages(t, ctx, connector.Pool()) {
		if !strings.HasPrefix(message.Topic, "user.") {
			continue
		}
		event, err := domainevent.Decode(ctx, message)
		if err != nil {
			t.Fatal(err)
		}
		var payload user.EventPayload
		if err := json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.UserID == created.ID {
			messages = append(messages, message)
		}
	}
	var createdMessage eventbus.Message
	for _, message := range messages {
		if strings.Contains(string(message.Body), "secret") || strings.Contains(string(message.Body), "password_hash") {
			t.Fatal("credential leaked into event")
		}
		if message.Topic == user.EventCreated {
			createdMessage = message
		}
	}
	if createdMessage.Topic == "" {
		t.Fatal("created event missing")
	}
	if err := runner.Handle(ctx, createdMessage); err == nil {
		t.Fatal("failed delivery acknowledged")
	}
	if successCalls != 1 || retryCalls != 1 {
		t.Fatal("independent recipients not executed")
	}
	// Reconstructing the runner simulates process restart; the source owns state.
	runner, err = entityhooks.NewRunner(database.EntityHookSources(), resolve, logger)
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Handle(ctx, createdMessage); err != nil {
		t.Fatal(err)
	}
	if successCalls != 1 || retryCalls != 2 {
		t.Fatal("completed recipient repeated or failed recipient lost")
	}
	for _, message := range messages {
		if err := runner.Handle(ctx, message); err != nil {
			t.Fatal(err)
		}
	}
	var event domainevent.Envelope
	if err := json.Unmarshal(createdMessage.Body, &event); err != nil {
		t.Fatal(err)
	}
	calls, err := database.EntityHookSources()[0].Calls(ctx, event.ID)
	if err != nil || len(calls) != 2 {
		t.Fatalf("calls=%d err=%v", len(calls), err)
	}
}

func hookMessages(t *testing.T, ctx context.Context, pool *pgxpool.Pool) []eventbus.Message {
	t.Helper()
	rows, err := pool.Query(ctx, `SELECT topic,message_key,body,headers FROM core.outbox_messages WHERE headers ? 'x-cms-entity-hooks-source' ORDER BY created_at,message_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var messages []eventbus.Message
	for rows.Next() {
		var message eventbus.Message
		var raw []byte
		if err := rows.Scan(&message.Topic, &message.Key, &message.Body, &raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &message.Headers); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, message)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return messages
}

func TestPostgresEntityHooksRevalidateGroupPermissions(t *testing.T) {
	_, database, ctx := openOutboxIntegrationDatabase(t)
	admin, err := database.Groups().Create(ctx, nil, group.Group{Code: fmt.Sprintf("protected%d", time.Now().UnixNano()), Name: "Protected", IsSuper: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := entityhooks.NewRegistry(entityhooks.Application, "")
	if err := entityhooks.RegisterBefore(registry.ForModule("policy", []string{"core"}), user.BeforeCreate, "inject-admin", func(_ context.Context, change *user.Change) error {
		change.GroupIDs = []group.ID{admin.ID}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Seal(); err != nil {
		t.Fatal(err)
	}
	groups, err := group.NewService(database.Groups(), hookRestrictedAccess{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	users, err := user.NewService(database.Users(), hookHasher{}, hookTestMedia{}, groups, hookRestrictedAccess{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	login := fmt.Sprintf("guard%d", time.Now().UnixNano())
	_, err = users.Create(ctx, security.System(), user.CreateInput{Login: login, Email: login + "@example.test", Password: "guard-secret-12345", Name: "Guard"})
	if !errors.Is(err, access.ErrNotPrivileged) {
		t.Fatalf("hook bypassed group authorization: %v", err)
	}
	if _, err := database.Users().ByIdentifier(ctx, login); !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("veto left a user: %v", err)
	}
}
