package app_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vernal96/go-cms-kernel"
	appkernel "github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/cache"
	"github.com/vernal96/go-cms-kernel/connectors/localstorage"
	"github.com/vernal96/go-cms-kernel/examples/counter"
	"github.com/vernal96/go-cms-kernel/filesystem"
	"github.com/vernal96/go-cms-kernel/modules/admin"
	"github.com/vernal96/go-cms-kernel/modules/core"
	"github.com/vernal96/go-cms-kernel/modules/core/field"
	"github.com/vernal96/go-cms-kernel/modules/core/media"
	"github.com/vernal96/go-cms-kernel/modules/core/user/adapters/argon2id"
	"github.com/vernal96/go-cms-kernel/modules/forms"
	"github.com/vernal96/go-cms-kernel/modules/mail"
	"github.com/vernal96/go-cms-kernel/modules/search"
	"github.com/vernal96/go-cms-kernel/modules/seo"
	"github.com/vernal96/go-cms-kernel/permission"
	"github.com/vernal96/go-cms-kernel/security"
)

type declarationMailRepository struct{ mail.Repository }
type declarationMailDatabase struct{}

func (declarationMailDatabase) ModuleCode() kernel.ModuleCode { return mail.ModuleCode }
func (declarationMailDatabase) Mail() mail.Repository         { return &declarationMailRepository{} }

type declarationFormsRepository struct{ forms.Repository }
type declarationFormsDatabase struct{}

func (*declarationFormsRepository) Kind() string { return "forms.element" }
func (*declarationFormsRepository) UpdatePermission() permission.Code {
	return forms.FormUpdatePermission
}
func (*declarationFormsRepository) ClearFileOccurrence(context.Context, *security.UserID, media.FileOccurrence) (int64, error) {
	return 0, media.ErrFileDeleteUnsupported
}

func (declarationFormsDatabase) ModuleCode() kernel.ModuleCode { return forms.ModuleCode }
func (declarationFormsDatabase) Forms() forms.Repository       { return &declarationFormsRepository{} }

type declarationSEODatabase struct{}

func (declarationSEODatabase) ModuleCode() kernel.ModuleCode    { return seo.ModuleCode }
func (declarationSEODatabase) ResourceMetadata() seo.Repository { return &declarationSEORepository{} }

type declarationSEORepository struct{ seo.Repository }
type declarationTransport struct{}

func (declarationTransport) Driver() string { return "test" }
func (declarationTransport) Send(context.Context, mail.Delivery) (mail.DeliveryResult, error) {
	panic("validation must not send mail")
}

func declarationMailConfig() mail.Config {
	return mail.Config{SendMaxAttempts: 3, MaxRecipients: 10, MaxMessageSize: 1024, MaxAttachmentSize: 1024}
}
func declarationFormsConfig() forms.Config {
	return forms.Config{ActionMaxAttempts: 3, DefaultCaptchaProvider: "development", ElementImage: field.FileOptions{Disk: "private", VirtualPath: "forms/images", SettingsCode: "forms_image", MIMETypes: []string{"image/*"}}, Public: forms.PublicLimits{
		MaxRequestSize: 1024, MaxScalarFields: 10, MaxScalarValueSize: 100, MaxUploadFileSize: 100, MaxUploadCount: 2, MaxTotalUploadBytes: 200, SubmissionTimeout: time.Second, RateLimit: 10, RateWindow: time.Minute, RateEntries: 100,
	}}
}

func declarationCoreConfig() core.Config {
	return core.Config{MediaSettings: []media.SettingsDefinition{{Code: "forms_image", Fields: []field.Definition{{Key: "title", Type: field.TypeString, Label: "Title"}}}}}
}

func TestBootValidatesEveryModuleWithoutSites(t *testing.T) {
	for _, test := range []struct {
		name    string
		modules func() []kernel.Module
		alter   func(*appkernel.Definition)
		want    string
	}{
		{name: "valid profiles without sites", modules: func() []kernel.Module {
			return []kernel.Module{mail.New(declarationMailConfig()), forms.New(declarationFormsConfig()), seo.New(seo.Config{}), counter.New(7)}
		}},
		{name: "counter value", modules: func() []kernel.Module { return []kernel.Module{counter.New(0)} }, want: "counter limit"},
		{name: "cache store", alter: func(d *appkernel.Definition) {
			d.Profiles[0].Modules[0] = core.New(core.Config{Caches: []cache.Binding{{Alias: core.HotCacheAlias, Code: "missing"}}})
		}, want: "missing"},
		{name: "cache alias", alter: func(d *appkernel.Definition) {
			d.Profiles[0].Modules[0] = core.New(core.Config{Caches: []cache.Binding{{Alias: "unknown", Code: "cache"}}})
			d.Caches = []cache.Factory{fakeCacheFactory{store: &fakeCacheStore{code: "cache"}}}
		}, want: "unknown core cache alias"},
		{name: "core limits", alter: func(d *appkernel.Definition) {
			d.Profiles[0].Modules[0] = core.New(core.Config{RepositoryCacheTTL: -time.Second})
		}, want: "cache TTL"},
		{name: "module dependency", modules: func() []kernel.Module { return []kernel.Module{forms.New(declarationFormsConfig())} }, want: "mail"},
		{name: "database adapter", modules: func() []kernel.Module { return []kernel.Module{search.New()} }, want: "database for module"},
		{name: "mail application", modules: func() []kernel.Module { return []kernel.Module{mail.New(declarationMailConfig())} }, alter: func(d *appkernel.Definition) { d.ModuleApplications = nil }, want: "application dependency"},
		{name: "mail limits", modules: func() []kernel.Module { return []kernel.Module{mail.New(mail.Config{})} }, want: "maximum attempts"},
		{name: "mail spool", modules: func() []kernel.Module {
			c := declarationMailConfig()
			c.SpoolEnabled = true
			c.SpoolTTL = time.Minute
			c.SpoolCleanupInterval = time.Second
			c.SpoolCleanupBatch = 1
			return []kernel.Module{mail.New(c)}
		}, want: "spool filesystem binding"},
		{name: "mail disk", modules: func() []kernel.Module {
			c := declarationMailConfig()
			c.UploadStorage = "missing"
			return []kernel.Module{mail.New(c)}
		}, want: "upload storage"},
		{name: "mail renderer", modules: func() []kernel.Module {
			c := declarationMailConfig()
			c.Renderer.MaxTemplateLength = -1
			return []kernel.Module{mail.New(c)}
		}, want: "rendering limits"},
		{name: "mail message ID", modules: func() []kernel.Module {
			c := declarationMailConfig()
			c.MessageIDDomain = "bad\r\ndomain"
			return []kernel.Module{mail.New(c)}
		}, want: "Message-ID"},
		{name: "forms spool", modules: func() []kernel.Module {
			c := declarationFormsConfig()
			c.SpoolEnabled = true
			c.SpoolTTL = time.Minute
			c.SpoolCleanupInterval = time.Second
			c.SpoolCleanupBatch = 1
			return []kernel.Module{mail.New(declarationMailConfig()), forms.New(c)}
		}, want: "spool filesystem binding"},
		{name: "forms captcha", modules: func() []kernel.Module {
			c := declarationFormsConfig()
			c.DefaultCaptchaProvider = "missing"
			return []kernel.Module{mail.New(declarationMailConfig()), forms.New(c)}
		}, want: "CAPTCHA provider"},
		{name: "SEO limits", modules: func() []kernel.Module { return []kernel.Module{seo.New(seo.Config{MaxResultLength: -1})} }, want: "renderer limits"},
	} {
		t.Run(test.name, func(t *testing.T) {
			closed := false
			connector := newFakeConnector("main")
			connector.onClose = func() { closed = true }
			definition := appkernel.Definition{
				Logger: fakeLoggerFactory{}, PasswordHasher: argon2id.Factory{}, SiteAccessPolicy: admin.AllowAllSitesPolicy{}, EventBus: fakeEventBusFactory{},
				MainDatabase: appkernel.DatabaseDefinition{Connector: &fakeConnectorFactory{connector: connector}, Adapters: []kernel.ModuleDatabaseFactory{
					&fakeDatabaseFactory{code: core.ModuleCode, database: &fakeCoreDatabase{repository: &fakeSiteRepository{}}},
					&fakeDatabaseFactory{code: mail.ModuleCode, database: declarationMailDatabase{}},
					&fakeDatabaseFactory{code: forms.ModuleCode, database: declarationFormsDatabase{}},
					&fakeDatabaseFactory{code: seo.ModuleCode, database: declarationSEODatabase{}},
				}},
				Filesystems:        []filesystem.Factory{localstorage.Factory{Config: localstorage.Config{Code: "private", Visibility: filesystem.VisibilityPrivate, Root: t.TempDir(), BaseURL: "http://files.test", SigningKey: strings.Repeat("s", 32)}}},
				ModuleApplications: []kernel.ModuleApplication{mail.Application{Transport: declarationTransport{}}, forms.Application{Providers: []forms.CaptchaProvider{forms.DevelopmentCaptchaProvider{ExpectedToken: "test"}}}},
				Profiles:           []kernel.Profile{{Code: "unused", Modules: []kernel.Module{core.New(declarationCoreConfig()), admin.New()}}},
			}
			if test.modules != nil {
				definition.Profiles[0].Modules = append(definition.Profiles[0].Modules, test.modules()...)
			}
			if test.alter != nil {
				test.alter(&definition)
			}
			application, err := appkernel.New(context.Background(), definition)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := application.Close(); err != nil {
					t.Error(err)
				}
			})
			err = application.Boot(context.Background())
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) || !strings.Contains(err.Error(), `"unused"`) {
				t.Fatalf("Boot error = %v; want profile and %q", err, test.want)
			}
			if application.Sites() != nil {
				t.Fatal("failed application exposed a site catalog")
			}
			if err := application.Close(); err != nil {
				t.Fatal(err)
			}
			if !closed {
				t.Fatal("database remained open")
			}
		})
	}
}
