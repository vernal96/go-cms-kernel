package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
	"time"

	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core/access"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/group"
	"github.com/vernal96/go-cms-kernel/modules/core/management"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/user"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
	httptransport "github.com/vernal96/go-cms-kernel/transport/http"
)

// Query-count assertions require pg_stat_statements and an isolated database:
// CMS_TEST_POSTGRES_QUERY_COUNTS=1 go test -p 1 ...
func assertReadSQLCount(t *testing.T, ctx context.Context, conn *connectorpostgres.Connector, want int64, read func()) {
	t.Helper()
	enabled := os.Getenv("CMS_TEST_POSTGRES_QUERY_COUNTS") == "1"
	count := func() int64 {
		var calls int64
		if err := conn.Pool().QueryRow(ctx, `SELECT COALESCE(sum(calls),0)::bigint FROM pg_stat_statements WHERE dbid=(SELECT oid FROM pg_database WHERE datname=current_database()) AND query NOT LIKE '%pg_stat_statements%'`).Scan(&calls); err != nil {
			t.Fatal(err)
		}
		return calls
	}
	var before int64
	if enabled {
		before = count()
	}
	read()
	if enabled {
		if got := count() - before; got != want {
			t.Fatalf("SQL statements=%d, want %d", got, want)
		}
	}
}

func TestPostgresAllowedSingleQueryAndFreshPolicy(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	definitions, err := permission.Definitions("custom", []permission.Entity{{Code: "article"}})
	if err != nil {
		t.Fatal(err)
	}
	adminDefinitions, err := permission.Definitions("admin", []permission.Entity{{Code: "panel"}})
	if err != nil {
		t.Fatal(err)
	}
	definitions = append(definitions, adminDefinitions...)
	catalog, err := permission.NewCatalog(definitions)
	if err != nil {
		t.Fatal(err)
	}
	service, err := access.NewService(db.Access(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	read := permission.MustCode("custom", "article", permission.Read)
	update := permission.MustCode("custom", "article", permission.Update)
	remove := permission.MustCode("custom", "article", permission.Delete)
	suffix := fmt.Sprint(time.Now().UnixNano())
	var uid security.UserID
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.users(login,email,password_hash,name) VALUES($1,$2,'hash','Batch User') RETURNING id`, "batch-"+suffix, "batch-"+suffix+"@example.test").Scan(&uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = conn.Pool().Exec(context.Background(), `DELETE FROM core.users WHERE id=$1`, uid) })
	var siteOne, siteTwo site.ID
	for i, target := range []*site.ID{&siteOne, &siteTwo} {
		if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,domain) VALUES('dev',$1) RETURNING id`, fmt.Sprintf("batch-%s-%d.test", suffix, i)).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = conn.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=ANY($1)`, []int64{int64(siteOne), int64(siteTwo)})
	})
	for i, code := range []permission.Code{read, update} {
		item, err := db.Groups().Create(ctx, nil, group.Group{Code: fmt.Sprintf("batch-%s-%d", suffix, i), Name: "Batch"}, []permission.Code{code, admin.AccessPermission}, []group.SiteAccess{{SiteID: siteOne, CanView: true, CanEdit: true}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = conn.Pool().Exec(context.Background(), `DELETE FROM core.groups WHERE id=$1`, item.ID) })
		if _, err := conn.Pool().Exec(ctx, `INSERT INTO core.user_groups(user_id,group_id) VALUES($1,$2)`, uid, item.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Access().GrantGuest(ctx, nil, remove); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Access().RevokeGuest(context.Background(), remove) })
	assertReadSQLCount(t, ctx, conn, 1, func() {
		got, err := service.Allowed(ctx, security.User(uid), []permission.Code{update, read, update, remove})
		if err != nil || !slices.Equal(got, []permission.Code{update, read}) {
			t.Fatalf("group union=%v, %v", got, err)
		}
	})
	assertReadSQLCount(t, ctx, conn, 1, func() {
		got, err := service.Allowed(ctx, security.Guest(), []permission.Code{read, remove})
		if err != nil || !slices.Equal(got, []permission.Code{remove}) {
			t.Fatalf("guest=%v, %v", got, err)
		}
	})
	// The endpoint performs one guard check, one profile read and one bulk permission read.
	runtime, err := admin.NewRuntime(integrationSessionUsers{repository: db.Users()}, service)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := runtime.SessionHandler()
	if err != nil {
		t.Fatal(err)
	}
	assertReadSQLCount(t, ctx, conn, 3, func() {
		request := httptest.NewRequest(http.MethodGet, "/api/admin/session", nil)
		request = request.WithContext(httptransport.WithActor(ctx, security.User(uid)))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("session=%d: %s", response.Code, response.Body.String())
		}
		var payload struct {
			Permissions []permission.Code `json:"permissions"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(payload.Permissions, []permission.Code{admin.AccessPermission, read, update}) {
			t.Fatalf("session permissions=%v", payload.Permissions)
		}
	})
	policy, err := management.NewGroupSiteAccessPolicy(db.Groups(), service)
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Check(ctx, security.User(uid), siteOne, management.SiteAccessEdit); err != nil {
		t.Fatal(err)
	}
	if err := policy.Check(ctx, security.User(uid), siteTwo, management.SiteAccessEdit); !errors.Is(err, security.ErrForbidden) {
		t.Fatalf("cross-site=%v", err)
	}
	if _, err := conn.Pool().Exec(ctx, `DELETE FROM core.group_permissions WHERE group_id IN(SELECT group_id FROM core.user_groups WHERE user_id=$1) AND permission_code=$2`, uid, read); err != nil {
		t.Fatal(err)
	}
	if got, err := service.Allowed(ctx, security.User(uid), []permission.Code{read, update}); err != nil || !slices.Equal(got, []permission.Code{update}) {
		t.Fatalf("revocation=%v, %v", got, err)
	}
	if _, err := conn.Pool().Exec(ctx, `DELETE FROM core.user_groups WHERE user_id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if got, err := service.Allowed(ctx, security.User(uid), []permission.Code{read, remove}); err != nil || !slices.Equal(got, []permission.Code{remove}) {
		t.Fatalf("ungrouped=%v, %v", got, err)
	}
	if _, err := conn.Pool().Exec(ctx, `UPDATE core.users SET blocked_at=now() WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Allowed(ctx, security.User(uid), []permission.Code{remove}); !errors.Is(err, security.ErrUnauthenticated) {
		t.Fatalf("blocked=%v", err)
	}
}

func TestPostgresLibraryPageIncludesVersionsWithoutExtraQuery(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	var sid site.ID
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,domain) VALUES('dev',$1) RETURNING id`, fmt.Sprintf("versions-%d.test", time.Now().UnixNano())).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Sites().(site.ManagementRepository).Delete(context.Background(), sid) })
	path := "/"
	library, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: sid, Type: resourcetype.Library, Title: "Versions", Path: &path, IsPublic: true, TypeSettings: map[string]any{"item_url_pattern": "/{slug}"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Resources().Delete(context.Background(), library.ID) })
	repo := db.Resources().(resource.LibraryItemRepository)
	ids := []resource.ID{}
	for i := 0; i < 4; i++ {
		item := resource.LibraryItem{SiteID: sid, LibraryID: library.ID, Title: fmt.Sprintf("Item %d", i), Slug: fmt.Sprintf("item-%d", i), IsPublic: true}
		if i < 2 {
			item.FieldValues = []field.StoredValue{{Key: "rank", Kind: field.StorageInteger, Value: int64(i)}}
		}
		created, err := repo.CreateLibraryItem(ctx, nil, item, false)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, created.ID)
	}
	current, err := repo.LibraryItemByID(ctx, ids[0])
	if err != nil {
		t.Fatal(err)
	}
	next := current
	next.Title = "Updated"
	updated, err := repo.UpdateLibraryItem(ctx, nil, current, next, false)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version != 2 {
		t.Fatalf("updated version=%d", updated.Version)
	}
	query := resource.LibraryItemQuery{SiteID: sid, LibraryID: library.ID, Limit: 3, Sort: []resource.Sort{{Field: resource.FieldID, Direction: resource.SortAscending}}}
	check := func(query resource.LibraryItemQuery, wantIDs []resource.ID, wantQueries int64) resource.LibraryItemPage {
		t.Helper()
		var page resource.LibraryItemPage
		assertReadSQLCount(t, ctx, conn, wantQueries, func() {
			var err error
			page, err = repo.QueryLibraryItems(ctx, query)
			if err != nil {
				t.Fatal(err)
			}
		})
		got := []resource.ID{}
		for _, item := range page.Items {
			got = append(got, item.ID)
			want := int64(1)
			if item.ID == ids[0] {
				want = 2
			}
			if item.Version != want {
				t.Fatalf("item %d version=%d, want %d", item.ID, item.Version, want)
			}
		}
		if !slices.Equal(got, wantIDs) {
			t.Fatalf("ids=%v, want %v", got, wantIDs)
		}
		return page
	}
	// Main rows, fields and widgets: no fourth version query.
	page := check(query, ids[:3], 3)
	if page.NextCursor == "" {
		t.Fatal("missing cursor")
	}
	query.Cursor = page.NextCursor
	last := check(query, ids[3:], 3)
	if last.NextCursor != "" {
		t.Fatal("unexpected cursor")
	}
	query.Cursor = ""
	query.Filters = []resource.FilterCondition{{Field: resource.FieldID, Operator: resource.FilterEqual, Value: int64(-1)}}
	check(query, nil, 1)
	query.Filters = nil
	query.Sort = []resource.Sort{{Field: "resource.field.rank", Kind: field.StorageInteger, Direction: resource.SortAscending}}
	page = check(query, ids[:3], 4) // Present and missing-value branches plus fields/widgets.
	if page.NextCursor == "" {
		t.Fatal("missing custom cursor")
	}
	query.Cursor = page.NextCursor
	check(query, ids[3:], 3)
	query.Cursor = ""
	query.Limit = 1
	page = check(query, ids[:1], 3)
	query.Cursor = page.NextCursor
	page = check(query, ids[1:2], 4) // Boundary between present values and the missing tail.
	query.Cursor = page.NextCursor
	check(query, ids[2:3], 4)
	query.Cursor = ""
	query.Limit = 3
	query.Sort = []resource.Sort{{Field: "resource.field.rank", Kind: field.StorageInteger, Direction: resource.SortDescending}, {Field: resource.FieldID, Direction: resource.SortDescending}}
	page = check(query, []resource.ID{ids[1], ids[0], ids[3]}, 4)
	query.Cursor = page.NextCursor
	check(query, []resource.ID{ids[2]}, 3)
	query.Cursor = ""
	query.Sort = []resource.Sort{{Field: resource.FieldTitle, Direction: resource.SortAscending}, {Field: "resource.field.rank", Kind: field.StorageInteger, Direction: resource.SortDescending}}
	page = check(query, []resource.ID{ids[1], ids[2], ids[3]}, 3)
	query.Cursor = page.NextCursor
	check(query, []resource.ID{ids[0]}, 3)
}

// Only the current-user read is needed by SessionHandler; other user operations
// remain unavailable in this integration fixture.
type integrationSessionUsers struct {
	user.Service
	repository user.Repository
}

func (s integrationSessionUsers) Current(ctx context.Context, actor security.Actor) (user.User, error) {
	id, ok := actor.UserID()
	if !ok {
		return user.User{}, security.ErrUnauthenticated
	}
	record, err := s.repository.ByID(ctx, id)
	if err != nil {
		return user.User{}, err
	}
	if record.BlockedAt != nil {
		return user.User{}, security.ErrUnauthenticated
	}
	return record.User, nil
}
