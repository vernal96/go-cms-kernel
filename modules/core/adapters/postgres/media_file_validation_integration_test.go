package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/resource"
	"github.com/vernal96/go-cms-kernel/modules/core/resourcetype"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/core/template"
	"github.com/vernal96/go-cms-kernel/modules/core/widget"
)

func TestPostgresMediaUpdateValidatesNormalizedOccurrencesWithoutImageMisclassification(t *testing.T) {
	conn, db, ctx := openOutboxIntegrationDatabase(t)
	suffix := fmt.Sprint(time.Now().UnixNano())
	var sid site.ID
	var siteVersion int64
	if err := conn.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,name,domain) VALUES('guard','Guard',$1) RETURNING id,runtime_version`, "media-guard-"+suffix+".test").Scan(&sid, &siteVersion); err != nil {
		t.Fatal(err)
	}
	f, err := db.Files().CreateFile(ctx, file.File{Storage: "public", Name: "guard-" + suffix + ".pdf", Path: "guard-" + suffix, MIMEType: "application/pdf", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1)})
	if err != nil {
		t.Fatal(err)
	}
	m, err := db.Media().Create(ctx, nil, media.Media{FileID: f.ID, Params: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup := context.Background()
		conn.Pool().Exec(cleanup, `DELETE FROM core.file_field_references WHERE (owner_kind='site' AND owner_id=$1) OR (owner_kind='resource' AND owner_id IN(SELECT id FROM core.resource_entities WHERE site_id=$1))`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.sites WHERE id=$1`, sid)
		conn.Pool().Exec(cleanup, `DELETE FROM core.media WHERE id=$1`, m.ID)
		conn.Pool().Exec(cleanup, `DELETE FROM core.files WHERE id=$1`, f.ID)
	})
	options := field.FileOptions{Disk: "public", VirtualPath: "documents", SettingsCode: "document", MIMETypes: []string{"application/pdf"}}
	rows := []field.Definition{{Key: "rows", Label: "Rows", Type: field.TypeRepeater, Options: field.RepeaterOptions{Fields: []field.Definition{{Key: "icon", Label: "Icon", Type: field.TypeFile, Options: options}}}}}
	rowSchema, err := field.CompilePersistent(rows, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	rowValues, err := rowSchema.Validate(map[string]any{"rows": []any{map[string]any{"icon": int64(m.ID)}}})
	if err != nil {
		t.Fatal(err)
	}
	rowRefs, err := rowSchema.References(rowValues)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Sites().(site.ManagementRepository).Update(ctx, nil, site.Site{ID: sid, ProfileCode: "guard", Name: "Guard", Domain: "media-guard-" + suffix + ".test", Locale: "en", Version: siteVersion, Settings: rowValues, MediaOccurrences: rowRefs})
	if err != nil {
		t.Fatal(err)
	}
	options.Multiple = true
	docSchema, err := field.CompilePersistent([]field.Definition{{Key: "documents", Label: "Documents", Type: field.TypeFile, Options: options}}, field.StandardTypes())
	if err != nil {
		t.Fatal(err)
	}
	docValues, err := docSchema.Validate(map[string]any{"documents": []any{int64(m.ID), int64(m.ID)}})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := docSchema.StoredValues(docValues)
	if err != nil {
		t.Fatal(err)
	}
	path := "/"
	templateCode := template.Code("page")
	owner, err := db.Resources().Create(ctx, nil, resource.Resource{SiteID: sid, Type: resourcetype.Page, Template: &templateCode, Title: "PDF", Path: &path, Fields: docValues, FieldValues: stored}, nil)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := db.Resources().(resource.WidgetRepository).CreateWidget(ctx, nil, owner.ID, owner.Version, widget.Binding{
		Code: "fields_tile", Area: "body", Params: rowValues, References: rowRefs,
		Presentation: widget.Presentation{Columns: 12, Enabled: true},
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	title := "PDF metadata"
	next := media.Clone(m)
	next.Title = &title
	validate := func(target file.File) media.ValidateUsages {
		return func(ctx context.Context, usages []media.Usage) error {
			if len(usages) != 4 {
				t.Fatalf("want site, two selections and widget, got %#v", usages)
			}
			seenWidget := false
			for _, usage := range usages {
				if usage.Kind != media.FileFieldUsage || usage.Occurrence == nil {
					t.Fatalf("PDF FileField misclassified: %#v", usage)
				}
				ref := *usage.Occurrence
				reader := db.Resources().(media.FileOccurrenceReader)
				schema := docSchema
				if ref.OwnerKind == "site" {
					reader, schema = db.Sites().(media.FileOccurrenceReader), rowSchema
				} else if ref.Container == fmt.Sprintf("widget:%d", binding.ID) {
					schema, seenWidget = rowSchema, true
				}
				values, err := reader.ReadFileOccurrence(ctx, ref)
				if err != nil {
					return err
				}
				references, err := schema.StoredReferences(values.Values)
				if err != nil {
					return err
				}
				if err := media.ValidateFileOccurrence(ref, references, target); err != nil {
					return err
				}
			}
			if !seenWidget {
				t.Fatal("widget occurrence not enumerated")
			}
			return nil
		}
	}
	updated, err := db.Media().Update(ctx, nil, next, validate(f))
	if err != nil || updated.Title == nil || *updated.Title != title {
		t.Fatalf("PDF metadata update: %+v, %v", updated, err)
	}
	for _, target := range []file.File{{ID: f.ID, Storage: "private", MIMEType: "application/pdf"}, {ID: f.ID, Storage: "public", MIMEType: "image/png"}} {
		next.Title = nil
		var validation field.ValidationErrors
		if _, err := db.Media().Update(ctx, nil, next, validate(target)); !errors.As(err, &validation) {
			t.Fatalf("incompatible file passed guard: %v", err)
		}
		current, err := db.Media().ByID(ctx, m.ID)
		if err != nil || current.Title == nil || *current.Title != title || !current.UpdatedAt.Equal(updated.UpdatedAt) {
			t.Fatalf("rejected update mutated Media: %+v, %v", current, err)
		}
	}
}
