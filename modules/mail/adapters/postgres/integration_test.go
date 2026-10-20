package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel"
	connectorpostgres "github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/eventbus"
	"github.com/vernal96/go-cms-kernel/job"
	"github.com/vernal96/go-cms-kernel/migrations"
	corepostgres "github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core/adapters/postgres/mediaoccurrence"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/field/validation"
	"github.com/vernal96/go-cms-kernel/modules/core/file"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/site"
	"github.com/vernal96/go-cms-kernel/modules/mail"
)

func TestPostgresMailCRUDOutboxAttemptsRetentionAndSiteIsolation(t *testing.T) {
	host := os.Getenv("CMS_TEST_MAIL_POSTGRES_HOST")
	if host == "" {
		t.Skip("set CMS_TEST_MAIL_POSTGRES_HOST to run the Mail PostgreSQL integration test")
	}
	port := 5432
	if raw := os.Getenv("CMS_TEST_MAIL_POSTGRES_PORT"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil {
			t.Fatalf("parse CMS_TEST_MAIL_POSTGRES_PORT: %v", err)
		}
		port = value
	}
	sslMode := os.Getenv("CMS_TEST_MAIL_POSTGRES_SSL_MODE")
	if sslMode == "" {
		sslMode = "disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	connector, err := connectorpostgres.New(ctx, connectorpostgres.Config{
		Code: "mail-integration", Host: host, Port: port,
		Database: os.Getenv("CMS_TEST_MAIL_POSTGRES_DB"), User: os.Getenv("CMS_TEST_MAIL_POSTGRES_USER"), Password: os.Getenv("CMS_TEST_MAIL_POSTGRES_PASSWORD"),
		SSLMode: sslMode, MaxConns: 4, ConnectTimeout: 5 * time.Second, ConnMaxLifetime: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connector.Close() })
	coreDatabase, err := corepostgres.NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	database, err := NewDatabase(connector)
	if err != nil {
		t.Fatal(err)
	}
	manager := migrations.NewManager()
	for _, source := range []migrations.Source{coreDatabase.MigrationSources()[0], database.MigrationSources()[0]} {
		if err := manager.Up(ctx, migrations.Plan{Connection: string(connector.Code()), Target: connector, Source: source}); err != nil {
			t.Fatalf("migrate %s: %v", source.ID, err)
		}
	}

	suffix := time.Now().UnixNano()
	siteIDs := make([]site.ID, 2)
	for index := range siteIDs {
		if err := connector.Pool().QueryRow(ctx, `INSERT INTO core.sites(profile_code,name,domain,locale,settings,is_public) VALUES('mail-test','Test site',$1,'en-US','{}'::jsonb,true) RETURNING id;`, fmt.Sprintf("mail-%d-%d.example.test", suffix, index)).Scan(&siteIDs[index]); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		_, _ = connector.Pool().Exec(context.Background(), `DELETE FROM core.sites WHERE id=ANY($1::bigint[]);`, []int64{int64(siteIDs[0]), int64(siteIDs[1])})
	})

	repository := database.Mail()
	t.Run("static attachment Media occurrence protects other links and clears atomically", func(t *testing.T) {
		storedFile, err := coreDatabase.Files().CreateFile(ctx, file.File{
			Storage: "private", Name: fmt.Sprintf("mail-attachment-%d.pdf", suffix), Path: fmt.Sprintf("mail-attachment/%d.pdf", suffix),
			MIMEType: "application/pdf", Size: 1, ChecksumSHA256: fmt.Sprintf("%064d", 1),
		})
		if err != nil {
			t.Fatal(err)
		}
		selected, err := coreDatabase.Media().Create(ctx, nil, media.Media{FileID: storedFile.ID, Params: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		item, err := repository.CreateTemplate(ctx, mail.Template{
			SiteID: siteIDs[0], Code: fmt.Sprintf("static_%d", suffix), Name: "Static attachment", Enabled: true,
			From: mail.AddressTemplate{Email: "noreply@example.test"}, ContentType: mail.ContentText,
			Attachments: []mail.AttachmentTemplate{{Source: mail.AttachmentStatic, MediaID: &selected.ID}},
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = repository.DeleteTemplate(context.Background(), siteIDs[0], item.ID)
			_, _ = connector.Pool().Exec(context.Background(), `DELETE FROM core.media WHERE id=$1`, selected.ID)
			_, _ = connector.Pool().Exec(context.Background(), `DELETE FROM core.files WHERE id=$1`, storedFile.ID)
		})
		refs, err := coreDatabase.FileDeletions().FileOccurrences(ctx, selected.ID)
		if err != nil || len(refs) != 1 || refs[0].OwnerKind != "mail_template" || refs[0].OwnerID != int64(item.ID) || refs[0].Container != "attachments" || len(refs[0].Path) != 2 || refs[0].Path[1] != "media_id" {
			t.Fatalf("Mail occurrence = %#v, %v", refs, err)
		}
		if _, err := repository.UpdateTemplate(ctx, item); err != nil {
			t.Fatalf("unchanged owner update rejected its existing Media occurrence: %v", err)
		}
		otherOwner := mail.Template{
			SiteID: siteIDs[0], Code: fmt.Sprintf("duplicate_owner_%d", suffix), Name: "Duplicate owner", Enabled: true,
			From: mail.AddressTemplate{Email: "noreply@example.test"}, ContentType: mail.ContentText,
			Attachments: []mail.AttachmentTemplate{{Source: mail.AttachmentStatic, MediaID: &selected.ID}},
		}
		if _, err := repository.CreateTemplate(ctx, otherOwner); !errors.Is(err, media.ErrAlreadyAttached) {
			t.Fatalf("second template reused an already attached Media: %v", err)
		}
		duplicateRows := otherOwner
		duplicateRows.Code = fmt.Sprintf("duplicate_rows_%d", suffix)
		duplicateRows.Attachments = append(duplicateRows.Attachments, duplicateRows.Attachments[0])
		if _, err := repository.CreateTemplate(ctx, duplicateRows); !errors.Is(err, media.ErrAlreadyAttached) {
			t.Fatalf("duplicate attachment rows reused one Media: %v", err)
		}
		refs, err = coreDatabase.FileDeletions().FileOccurrences(ctx, selected.ID)
		if err != nil || len(refs) != 1 || refs[0].OwnerID != int64(item.ID) {
			t.Fatalf("failed duplicate writes changed the saved owner occurrence: %#v, %v", refs, err)
		}
		if _, err := connector.Pool().Exec(ctx, `INSERT INTO core.media_field_occurrences(owner_kind,owner_id,site_id,container,value_path,media_id,reference_target) VALUES('resource',$1,$2,'other',ARRAY['asset'],$3,'media')`, int64(item.ID)+9000000, siteIDs[0], selected.ID); err != nil {
			t.Fatal(err)
		}
		_, err = coreDatabase.FileDeletions().DeleteMediaFile(
			ctx,
			nil,
			media.DeleteFileInput{
				SiteID: int64(siteIDs[0]), MediaID: selected.ID,
				ExpectedFileID: storedFile.ID, ExpectedUpdatedAt: selected.UpdatedAt,
			},
			func(context.Context, *media.FileOccurrence) (media.PreparedFileOwner, error) {
				return media.PreparedFileOwner{}, errors.New("owner must not be prepared with another reference")
			})
		if !errors.Is(err, media.ErrFileInUse) {
			t.Fatalf("other owner did not block physical deletion: %v", err)
		}
		if _, err := connector.Pool().Exec(ctx, `DELETE FROM core.media_field_occurrences WHERE owner_kind='resource' AND media_id=$1`, selected.ID); err != nil {
			t.Fatal(err)
		}
		tx, err := connector.Pool().Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		owner := repository.(mail.FileOccurrenceCleaner)
		if _, err := owner.ClearFileOccurrence(mediaoccurrence.WithTransaction(ctx, connector.Pool(), tx), nil, refs[0]); err != nil {
			_ = tx.Rollback(context.Background())
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		saved, err := repository.TemplateByID(ctx, siteIDs[0], item.ID)
		if err != nil || len(saved.Attachments) != 0 {
			t.Fatalf("saved static attachment after clear = %#v, %v", saved.Attachments, err)
		}
		refs, err = coreDatabase.FileDeletions().FileOccurrences(ctx, selected.ID)
		if err != nil || len(refs) != 0 {
			t.Fatalf("occurrences after clear = %#v, %v", refs, err)
		}
		item.Attachments = []mail.AttachmentTemplate{{Source: mail.AttachmentStatic, MediaID: &selected.ID}}
		if _, err := repository.UpdateTemplate(ctx, item); err != nil {
			t.Fatal(err)
		}
		if err := repository.DeleteTemplate(ctx, siteIDs[0], item.ID); err != nil {
			t.Fatal(err)
		}
		refs, err = coreDatabase.FileDeletions().FileOccurrences(ctx, selected.ID)
		if err != nil || len(refs) != 0 {
			t.Fatalf("occurrences after template delete = %#v, %v", refs, err)
		}
	})
	t.Run("integer validator precision survives create update and reload", func(t *testing.T) {
		const initial int64 = 9007199254740993
		item, err := repository.CreateTemplate(ctx, mail.Template{
			SiteID: siteIDs[0], Code: "precise", Name: "Precise", Enabled: true,
			From: mail.AddressTemplate{Email: "noreply@example.test"}, To: []mail.AddressTemplate{{Email: "recipient@example.test"}},
			Subject: "Precise", ContentType: mail.ContentText, TextBody: "Ready",
			Variables: []field.Definition{{Key: "number", Type: field.TypeInteger, Label: "Number", Validators: []field.ValidatorDefinition{validation.In(initial)}}},
		})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := repository.DeleteTemplate(ctx, siteIDs[0], item.ID); err != nil {
				t.Error(err)
			}
		}()
		for _, allowed := range []int64{initial, initial + 2} {
			if allowed != initial {
				item.Variables[0].Validators = []field.ValidatorDefinition{validation.In(allowed)}
				if _, err := repository.UpdateTemplate(ctx, item); err != nil {
					t.Fatal(err)
				}
			}
			saved, err := repository.TemplateByID(ctx, siteIDs[0], item.ID)
			if err != nil {
				t.Fatal(err)
			}
			schema, err := field.Compile(saved.Variables, field.StandardTypes())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := schema.Validate(map[string]any{"number": allowed}); err != nil {
				t.Fatal(err)
			}
			if _, err := schema.Validate(map[string]any{"number": allowed - 1}); err == nil {
				t.Fatal("rounded integer accepted")
			}
		}
	})
	template, err := repository.CreateTemplate(ctx, mail.Template{
		SiteID: siteIDs[0], Code: "invoice", Name: "Invoice", Enabled: true,
		From: mail.AddressTemplate{Email: "noreply@example.test"}, To: []mail.AddressTemplate{{Email: "{{data.email}}"}},
		Subject: "Invoice", ContentType: mail.ContentText, TextBody: "Ready",
		Variables: []field.Definition{{Key: "email", Type: field.TypeEmail, Label: "Email"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	otherTemplate, err := repository.CreateTemplate(ctx, mail.Template{
		SiteID: siteIDs[1], Code: "invoice", Name: "Other Invoice", Enabled: true,
		From: mail.AddressTemplate{Email: "noreply@example.test"}, To: []mail.AddressTemplate{{Email: "{{data.email}}"}},
		Subject: "Other Invoice", ContentType: mail.ContentText, TextBody: "Other",
		Variables: []field.Definition{{Key: "email", Type: field.TypeEmail, Label: "Email"}},
	})
	if err != nil {
		t.Fatalf("same template code on another site: %v", err)
	}
	resolvedFirst, err := repository.TemplateByCode(ctx, siteIDs[0], "invoice")
	if err != nil || resolvedFirst.ID != template.ID {
		t.Fatalf("site A template by code = %#v, %v", resolvedFirst, err)
	}
	resolvedSecond, err := repository.TemplateByCode(ctx, siteIDs[1], "invoice")
	if err != nil || resolvedSecond.ID != otherTemplate.ID || resolvedSecond.ID == template.ID {
		t.Fatalf("site B template by code = %#v, %v", resolvedSecond, err)
	}
	if _, err := repository.CreateTemplate(ctx, mail.Template{
		SiteID: siteIDs[0], Code: "invoice", Name: "Duplicate",
		Enabled: true, From: mail.AddressTemplate{}, ContentType: mail.ContentText,
	}); !errors.Is(err, mail.ErrConflict) {
		t.Fatalf("duplicate template error = %v", err)
	}
	if _, err := repository.TemplateByID(ctx, siteIDs[1], template.ID); !errors.Is(err, mail.ErrNotFound) {
		t.Fatalf("cross-site template read error = %v", err)
	}

	messageTemplateID := template.ID
	message := mail.Message{
		SiteID: siteIDs[0], TemplateID: &messageTemplateID, TemplateCode: template.Code, TemplateName: template.Name,
		RFCMessageID: fmt.Sprintf("<mail-%d@example.test>", suffix),
		From:         mail.Address{Email: "noreply@example.test"}, To: []mail.Address{{Email: "person@example.test"}},
		Subject: "Invoice", ContentType: mail.ContentText, TextBody: "Ready", Origin: mail.OriginManual, RequestedAt: time.Now().UTC(),
	}
	queuedJob, err := job.NewScoped(mail.SendJobName, 1, fmt.Sprint(siteIDs[0]), struct {
		MessageID mail.MessageID `json:"message_id"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	if active, err := repository.HasActiveMessages(ctx, siteIDs[0]); err != nil || active {
		t.Fatalf("Site A active before queue = %t, %v", active, err)
	}
	if active, err := repository.HasActiveMessages(ctx, siteIDs[1]); err != nil || active {
		t.Fatalf("Site B active before queue = %t, %v", active, err)
	}
	body, _ := json.Marshal(queuedJob)
	stored, err := repository.CreateMessageAndJob(ctx, message, eventbus.Message{
		Topic: job.Topic(mail.SendJobName), Key: []byte(queuedJob.ID), Body: body,
		Headers: map[string][]byte{"content-type": []byte("application/json")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if active, err := repository.HasActiveMessages(ctx, siteIDs[0]); err != nil || !active {
		t.Fatalf("Site A active after queued message = %t, %v", active, err)
	}
	if active, err := repository.HasActiveMessages(ctx, siteIDs[1]); err != nil || active {
		t.Fatalf("Site B active after Site A queued message = %t, %v", active, err)
	}
	var outboxCount int
	if err := connector.Pool().QueryRow(ctx, `SELECT count(*) FROM core.outbox_messages WHERE message_id=$1;`, queuedJob.ID).Scan(&outboxCount); err != nil || outboxCount != 1 {
		t.Fatalf("outbox count = %d, %v", outboxCount, err)
	}
	if _, err := repository.MessageDetail(ctx, siteIDs[1], stored.ID); !errors.Is(err, mail.ErrNotFound) {
		t.Fatalf("cross-site message read error = %v", err)
	}

	claimed, firstAttempt, ok, err := repository.ClaimMessage(ctx, siteIDs[0], stored.ID, 5)
	if err != nil || !ok || claimed.ID != stored.ID {
		t.Fatalf("first claim = %#v %#v %t %v", claimed, firstAttempt, ok, err)
	}
	if active, err := repository.HasActiveMessages(ctx, siteIDs[0]); err != nil || !active {
		t.Fatalf("sending message active = %t, %v", active, err)
	}
	if err := repository.FinishAttempt(ctx, stored.ID, firstAttempt.AttemptNumber, mail.DeliveryResult{Driver: "smtp"}, &mail.DeliveryError{Retryable: true, Code: "temporary", Err: errors.New("temporary")}, false); err != nil {
		t.Fatal(err)
	}
	if active, err := repository.HasActiveMessages(ctx, siteIDs[0]); err != nil || !active {
		t.Fatalf("retryable message active = %t, %v", active, err)
	}
	_, secondAttempt, ok, err := repository.ClaimMessage(ctx, siteIDs[0], stored.ID, 5)
	if err != nil || !ok || secondAttempt.AttemptNumber != 2 {
		t.Fatalf("retry claim = %#v %t %v", secondAttempt, ok, err)
	}
	if err := repository.FinishAttempt(ctx, stored.ID, secondAttempt.AttemptNumber, mail.DeliveryResult{Driver: "smtp", ResponseCode: "250", RemoteMessageID: "provider-1"}, nil, true); err != nil {
		t.Fatal(err)
	}
	if active, err := repository.HasActiveMessages(ctx, siteIDs[0]); err != nil || active {
		t.Fatalf("accepted-only site active = %t, %v", active, err)
	}
	otherMessage := message
	otherMessage.SiteID, otherMessage.TemplateID = siteIDs[1], nil
	otherMessage.RFCMessageID = fmt.Sprintf("<mail-other-%d@example.test>", suffix)
	otherJob, err := job.NewScoped(mail.SendJobName, 1, fmt.Sprint(siteIDs[1]), struct {
		MessageID mail.MessageID `json:"message_id"`
	}{})
	if err != nil {
		t.Fatal(err)
	}
	otherBody, _ := json.Marshal(otherJob)
	otherStored, err := repository.CreateMessageAndJob(ctx, otherMessage, eventbus.Message{
		Topic: job.Topic(mail.SendJobName), Key: []byte(otherJob.ID), Body: otherBody,
		Headers: map[string][]byte{"content-type": []byte("application/json")},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, otherAttempt, ok, err := repository.ClaimMessage(ctx, siteIDs[1], otherStored.ID, 5)
	if err != nil || !ok {
		t.Fatalf("other-site claim = %#v %t %v", otherAttempt, ok, err)
	}
	if err := repository.FinishAttempt(ctx, otherStored.ID, otherAttempt.AttemptNumber, mail.DeliveryResult{Driver: "smtp"}, &mail.DeliveryError{Code: "550", Err: errors.New("rejected")}, true); err != nil {
		t.Fatal(err)
	}
	if active, err := repository.HasActiveMessages(ctx, siteIDs[1]); err != nil || active {
		t.Fatalf("terminal failed message active = %t, %v", active, err)
	}
	page, err := repository.ListMessages(ctx, siteIDs[0], mail.MessageQuery{PageQuery: mail.PageQuery{Page: 1, PerPage: 20}})
	if err != nil || len(page.Items) != 1 || page.Items[0].AttemptCount != 2 || page.Items[0].LatestAttempt == nil || page.Items[0].LatestAttempt.Status != mail.AttemptAccepted {
		t.Fatalf("message list = %#v, %v", page, err)
	}
	if err := repository.DeleteTemplate(ctx, siteIDs[0], template.ID); err != nil {
		t.Fatal(err)
	}
	detail, err := repository.MessageDetail(ctx, siteIDs[0], stored.ID)
	if err != nil || detail.Message.TemplateID != nil || detail.Message.TemplateCode != "invoice" || len(detail.Attempts) != 2 {
		t.Fatalf("historical snapshot = %#v, %v", detail, err)
	}
	if _, err := connector.Pool().Exec(ctx, `UPDATE mail.messages SET updated_at=clock_timestamp()-interval '48 hours' WHERE id=ANY($1::bigint[]);`, []int64{int64(stored.ID), int64(otherStored.ID)}); err != nil {
		t.Fatal(err)
	}
	removed, err := repository.Cleanup(ctx, siteIDs[0], 24*time.Hour, 100)
	if err != nil || removed != 1 {
		t.Fatalf("cleanup removed = %d, %v", removed, err)
	}
	if _, err := repository.MessageDetail(ctx, siteIDs[0], stored.ID); !errors.Is(err, mail.ErrNotFound) {
		t.Fatalf("message after retention = %v", err)
	}
	if _, err := repository.MessageDetail(ctx, siteIDs[1], otherStored.ID); err != nil {
		t.Fatalf("other site's terminal message was removed: %v", err)
	}
	if _, err := connector.Pool().Exec(ctx, `UPDATE core.sites SET profile_code='without-mail' WHERE id=$1;`, siteIDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TemplateByCode(ctx, siteIDs[1], "invoice"); err != nil {
		t.Fatalf("profile removal deleted Mail template: %v", err)
	}
	if _, err := repository.MessageDetail(ctx, siteIDs[1], otherStored.ID); err != nil {
		t.Fatalf("profile removal deleted Mail history: %v", err)
	}
	deleteTemplate, err := repository.CreateTemplate(ctx, mail.Template{
		SiteID: siteIDs[0], Code: "delete_owned", Name: "Delete Owned", Enabled: false,
		From: mail.AddressTemplate{Email: "noreply@example.test"}, To: []mail.AddressTemplate{{Email: "person@example.test"}},
		Subject: "Delete", ContentType: mail.ContentText, TextBody: "Delete",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := connector.Pool().Exec(ctx, `DELETE FROM core.sites WHERE id=$1;`, siteIDs[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.TemplateByID(ctx, siteIDs[0], deleteTemplate.ID); !errors.Is(err, mail.ErrNotFound) {
		t.Fatalf("deleted site's template remains: %v", err)
	}
	if _, err := repository.TemplateByCode(ctx, siteIDs[1], "invoice"); err != nil {
		t.Fatalf("deleting Site A removed Site B template: %v", err)
	}
	if _, err := repository.MessageDetail(ctx, siteIDs[1], otherStored.ID); err != nil {
		t.Fatalf("deleting Site A removed Site B history: %v", err)
	}
}

var _ kernel.ModuleDatabase = (*Database)(nil)
