package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/domainevent"
	"github.com/vernal96/go-cms-kernel/entityhooks"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type cascadeHasherFactory struct{}

func (cascadeHasherFactory) Open() (user.PasswordHasher, error) { return hookHasher{}, nil }

type cascadeDisk struct {
	filesystem.Disk
	calls int
	err   error
}

func (d *cascadeDisk) Delete(context.Context, string) error { d.calls++; return d.err }

type cascadeDisks struct{ disk *cascadeDisk }

func (d cascadeDisks) Disk(filesystem.Code) (filesystem.Disk, bool) { return d.disk, true }
func (d cascadeDisks) Disks() []filesystem.DiskInfo                 { return nil }

type cascadeProfiles map[kernel.ProfileCode]*kernel.ProfileBlueprint

func (p cascadeProfiles) ProfileBlueprint(code kernel.ProfileCode) (*kernel.ProfileBlueprint, bool) {
	v, ok := p[code]
	return v, ok
}

// Limit the service catalog to this fixture; other integration cases use
// independent profile schemas in the same test database.
type cascadeDatabase struct {
	core.Database
	siteID site.ID
}

func (d cascadeDatabase) Sites() site.Repository {
	return cascadeSiteRepository{ManagementRepository: d.Database.Sites().(site.ManagementRepository), id: d.siteID}
}

type cascadeSiteRepository struct {
	site.ManagementRepository
	id site.ID
}

func (r cascadeSiteRepository) List(ctx context.Context) ([]site.Site, error) {
	s, err := r.FindByID(ctx, r.id)
	if err != nil {
		return nil, err
	}
	return []site.Site{s}, nil
}

func TestPostgresMediaCascadeHooksAtomicity(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	var siteID site.ID
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,domain) VALUES('dev',$1) RETURNING id`, "cascade-"+suffix+".test").Scan(&siteID); err != nil {
		t.Fatal(err)
	}
	var userID user.ID
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.users(login,email,password_hash,name) VALUES($1,$2,'hash','Cascade user') RETURNING id`, "cascade"+suffix, suffix+"@cascade.test").Scan(&userID); err != nil {
		t.Fatal(err)
	}
	var groupID int64
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.groups(code,name,is_super) VALUES($1,'Cascade operator',true) RETURNING id`, "cascade"+suffix).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.user_groups(user_id,group_id) VALUES($1,$2)`, userID, groupID); err != nil {
		t.Fatal(err)
	}
	actor := security.User(userID)
	t.Cleanup(func() {
		conn.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=$1`, siteID)
		conn.Pool().Exec(context.Background(), `DELETE FROM core.users WHERE id=$1`, userID)
		conn.Pool().Exec(context.Background(), `DELETE FROM core.groups WHERE id=$1`, groupID)
		conn.Pool().Exec(context.Background(), `DELETE FROM core.files WHERE path=$1`, suffix)
	})
	f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: suffix + ".png", Path: suffix, MIMEType: "image/png", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
	if err != nil {
		t.Fatal(err)
	}
	makeMedia := func() media.ID {
		t.Helper()
		m, e := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
		if e != nil {
			t.Fatal(e)
		}
		return m.ID
	}
	resourceMedia, fieldMedia, avatarMedia := makeMedia(), makeMedia(), makeMedia()
	var resourceID resource.ID
	if err := conn.Pool().QueryRow(ctx, `WITH e AS (INSERT INTO core.resource_entities(site_id,storage_kind) VALUES($1,'tree') RETURNING id) INSERT INTO core.resources(id,site_id,type,title,path,image_media_id,type_settings) SELECT id,$1,'library','Owner','/',$2,'{"item_url_pattern":"/{slug}"}'::jsonb FROM e RETURNING id`, siteID, resourceMedia).Scan(&resourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.resource_field_values(resource_id,site_id,field_key,value_kind,value_reference) VALUES($1,$2,'photo','reference',$3)`, resourceID, siteID, fieldMedia); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.resource_media_references(resource_id,field_key,position,media_id) VALUES($1,'photo',0,$2)`, resourceID, fieldMedia); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Pool().Exec(ctx, `UPDATE core.users SET avatar_media_id=$2 WHERE id=$1`, userID, avatarMedia); err != nil {
		t.Fatal(err)
	}
	itemImage, itemField := makeMedia(), makeMedia()
	library := db.Resources().(resource.LibraryItemRepository)
	item, err := library.CreateLibraryItem(ctx, nil, resource.LibraryItem{SiteID: siteID, LibraryID: resourceID, Title: "Item", Slug: "item", ImageMediaID: &itemImage, FieldValues: []field.StoredValue{{Key: "photo", Kind: field.StorageReference, ReferenceTarget: field.ReferenceMedia, Value: int64(itemField)}}}, false)
	if err != nil {
		t.Fatal(err)
	}
	mode := ""
	beforeResources, beforeUsers := 0, 0
	var deliveredResources []resource.EventPayload
	var deliveredUsers []user.EventPayload
	veto := errors.New("cascade veto")
	module := hookTestModule{before: func(_ context.Context, c *resource.Change) error {
		beforeResources++
		if c.Actor != actor || c.Operation != resource.OperationMediaCascade || c.Before == nil || c.Before.Data.ImageMediaID == nil || c.Data.ImageMediaID != nil {
			return errors.New("invalid cascade resource draft")
		}
		if _, ok := c.Data.Fields["photo"]; ok {
			return errors.New("media field survived in candidate")
		}
		if mode == "resource veto" {
			return veto
		}
		if mode == "rewrite" {
			c.Data.Title = "unrelated change"
		}
		return nil
	}, after: func(_ context.Context, _ entityhooks.Delivery, p resource.EventPayload) error {
		deliveredResources = append(deliveredResources, p)
		return nil
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	factory, err := kernel.NewProfileRuntimeFactory(hookTestResolver{}, kernel.RuntimeServices{EventBus: hookTestBus{}, Logger: logger})
	if err != nil {
		t.Fatal(err)
	}
	blueprint, err := factory.Compile(ctx, kernel.Profile{Code: "dev", Modules: []kernel.ProfileModule{{Module: module}}})
	if err != nil {
		t.Fatal(err)
	}
	profiles := cascadeProfiles{"dev": blueprint}
	appHooks := entityhooks.NewRegistry(entityhooks.Application, "")
	registrar := appHooks.ForModule("core", nil)
	if err := entityhooks.RegisterBefore(registrar, user.BeforeUpdate, "before_avatar", func(_ context.Context, c *user.Change) error {
		beforeUsers++
		if c.Actor != actor || c.Operation != user.OperationMediaCascade || c.Before == nil || c.Before.Data.AvatarMediaID == nil || c.Data.AvatarMediaID != nil {
			return errors.New("invalid avatar cascade draft")
		}
		if mode == "user veto" {
			return veto
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := entityhooks.RegisterAfter(registrar, user.AfterUpdate, "avatar", func(_ context.Context, _ entityhooks.Delivery, p user.EventPayload) error {
		deliveredUsers = append(deliveredUsers, p)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := appHooks.Seal(); err != nil {
		t.Fatal(err)
	}
	permissions, err := permission.NewCatalog([]permission.Definition{{Module: "core", Entity: "file", Action: permission.Read, Code: "core.file.read"}, {Module: "core", Entity: "file", Action: permission.Delete, Code: "core.file.delete"}})
	if err != nil {
		t.Fatal(err)
	}
	disk := &cascadeDisk{}
	services, err := core.NewServices(cascadeDatabase{Database: db, siteID: siteID}, permissions, cascadeDisks{disk}, cascadeHasherFactory{}, nil, appHooks)
	if err != nil {
		t.Fatal(err)
	}
	if err := services.BuildContent(ctx, profiles); err != nil {
		t.Fatal(err)
	}
	cascade := services.Files.(file.CascadeService)
	items := []file.ItemReference{{Kind: file.ItemFile, ID: int64(f.ID)}}
	impact, err := cascade.DeleteImpact(ctx, actor, items)
	if err != nil {
		t.Fatal(err)
	}
	// The same Media attached to a different set of owners invalidates approval.
	if _, err := conn.Pool().Exec(ctx, `UPDATE core.users SET avatar_media_id=NULL WHERE id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	if err := cascade.DeleteConfirmed(ctx, actor, items, impact.Token); !errors.Is(err, file.ErrConflict) {
		t.Fatal("stale owner impact accepted", err)
	}
	if disk.calls != 0 {
		t.Fatal("stale impact touched storage")
	}
	if _, err := conn.Pool().Exec(ctx, `UPDATE core.users SET avatar_media_id=$2 WHERE id=$1`, userID, avatarMedia); err != nil {
		t.Fatal(err)
	}
	var beforeOutbox, beforeCalls int
	if err := conn.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM core.outbox_messages),(SELECT count(*) FROM core.entity_hook_calls)`).Scan(&beforeOutbox, &beforeCalls); err != nil {
		t.Fatal(err)
	}
	assertRollback := func() {
		t.Helper()
		var version int64
		var image *int64
		var fields, events, calls int
		if err := conn.Pool().QueryRow(ctx, `SELECT e.version,r.image_media_id,(SELECT count(*) FROM core.resource_media_references WHERE resource_id=e.id),(SELECT count(*) FROM core.outbox_messages),(SELECT count(*) FROM core.entity_hook_calls) FROM core.resource_entities e JOIN core.resources r ON r.id=e.id WHERE e.id=$1`, resourceID).Scan(&version, &image, &fields, &events, &calls); err != nil {
			t.Fatal(err)
		}
		if version != 1 || image == nil || fields != 1 || events != beforeOutbox || calls != beforeCalls {
			t.Fatalf("rollback leaked state: version=%d image=%v fields=%d events=%d calls=%d", version, image, fields, events, calls)
		}
		var avatar *int64
		if err := conn.Pool().QueryRow(ctx, `SELECT avatar_media_id FROM core.users WHERE id=$1`, userID).Scan(&avatar); err != nil || avatar == nil {
			t.Fatal("avatar not rolled back", err)
		}
	}
	for _, failure := range []string{"resource veto", "user veto", "rewrite", "outbox", "physical"} {
		t.Run(failure, func(t *testing.T) {
			mode = failure
			disk.calls = 0
			if failure == "outbox" {
				if _, err := conn.Pool().Exec(ctx, `CREATE FUNCTION core.reject_media_cascade_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'outbox unavailable'; END $$; CREATE TRIGGER reject_media_cascade_event BEFORE INSERT ON core.outbox_messages FOR EACH ROW EXECUTE FUNCTION core.reject_media_cascade_event();`); err != nil {
					t.Fatal(err)
				}
			}
			if failure == "physical" {
				disk.err = errors.New("storage unavailable")
			}
			err := cascade.DeleteConfirmed(ctx, actor, items, impact.Token)
			if failure == "outbox" {
				if _, e := conn.Pool().Exec(ctx, `DROP TRIGGER reject_media_cascade_event ON core.outbox_messages; DROP FUNCTION core.reject_media_cascade_event()`); e != nil {
					t.Fatal(e)
				}
			}
			disk.err = nil
			if err == nil {
				t.Fatal("failed cascade succeeded")
			}
			if (failure == "resource veto" || failure == "user veto") && !errors.Is(err, veto) {
				t.Fatal("lost veto", err)
			}
			if failure != "physical" && disk.calls != 0 {
				t.Fatal("physical deletion happened before validation/outbox")
			}
			assertRollback()
		})
	}
	mode = ""
	disk.calls = 0
	if err := cascade.DeleteConfirmed(ctx, actor, items, impact.Token); err != nil {
		t.Fatal(err)
	}
	if disk.calls != 1 || beforeResources < 6 || beforeUsers < 3 {
		t.Fatalf("missing service lifecycle: disk=%d resource=%d user=%d", disk.calls, beforeResources, beforeUsers)
	}
	current, err := db.Resources().ByID(ctx, resourceID)
	if err != nil || current.Version != 2 || current.ImageMediaID != nil || len(current.Fields) != 0 {
		t.Fatalf("invalid committed resource: %+v %v", current, err)
	}
	history, err := db.Resources().(resource.RevisionRepository).ListRevisions(ctx, siteID, resourceID, 1, 10)
	if err != nil || history.Total != 1 || history.Items[0].Version != 2 {
		t.Fatal("cascade history missing", history, err)
	}
	itemHistory, err := db.Resources().(resource.RevisionRepository).ListRevisions(ctx, siteID, item.ID, 1, 10)
	if err != nil || itemHistory.Total != 1 || itemHistory.Items[0].Version != 2 || itemHistory.Items[0].CreatedBy == nil || *itemHistory.Items[0].CreatedBy != userID {
		t.Fatal("site LibraryItem history policy or actor lost", itemHistory, err)
	}
	if current.UpdatedBy == nil || *current.UpdatedBy != userID || history.Items[0].CreatedBy == nil || *history.Items[0].CreatedBy != userID {
		t.Fatal("resource cascade audit actor lost")
	}
	runner, err := entityhooks.NewRunner(db.EntityHookSources(), func(target entityhooks.Target) (*entityhooks.Registry, bool) {
		if target.Scope == entityhooks.Application {
			return appHooks, true
		}
		runtime, ok := services.Sites.RuntimeByID(siteID)
		if !ok {
			return nil, false
		}
		return runtime.Profile().EntityHooks(), true
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := conn.Pool().Query(ctx, `SELECT topic,message_key,body,headers FROM core.outbox_messages WHERE message_id IN (SELECT event_id FROM core.entity_hook_calls WHERE scope_id=$1 OR (scope='application' AND handler='avatar' AND event_body->'payload'->>'user_id'=$2))`, fmt.Sprint(siteID), fmt.Sprint(userID))
	if err != nil {
		t.Fatal(err)
	}
	var events []eventbus.Message
	for messages.Next() {
		var m eventbus.Message
		var headers []byte
		if err := messages.Scan(&m.Topic, &m.Key, &m.Body, &headers); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(headers, &m.Headers); err != nil {
			t.Fatal(err)
		}
		events = append(events, m)
	}
	messages.Close()
	if err := messages.Err(); err != nil {
		t.Fatal(err)
	}
	for _, m := range events {
		if _, err := domainevent.Decode(ctx, m); err != nil {
			t.Fatal(err)
		}
		if err := runner.Handle(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if len(deliveredResources) != 2 || len(deliveredUsers) != 1 {
		t.Fatalf("deliveries: resources=%d users=%d", len(deliveredResources), len(deliveredUsers))
	}
	p := deliveredResources[0]
	if p.ActorID == nil || *p.ActorID != userID || p.Before == nil || p.Before.Version != 1 || p.After == nil || p.After.Version != 2 || p.Operation != resource.OperationMediaCascade {
		t.Fatalf("bad resource event: %+v", p)
	}
	u := deliveredUsers[0]
	if u.ActorID == nil || *u.ActorID != userID || u.Before == nil || u.Before.Data.AvatarMediaID == nil || u.After.Data.AvatarMediaID != nil || u.Operation != user.OperationMediaCascade {
		t.Fatalf("bad user event: %+v", u)
	}
}
