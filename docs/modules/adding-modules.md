# Подключение встроенных модулей

Модуль добавляется в `kernel.Profile.Modules` приложения. Это декларация: конструктор не открывает БД, не создаёт диски и не регистрирует transport. Приложение отдельно передаёт database adapters в `app.Definition.MainDatabase.Adapters`, физические хранилища в `Filesystems`/`Caches`, а зависимости уровня приложения в `ModuleApplications`. В Starter эти места находятся в [профиле](https://github.com/vernal96/go-cms/blob/main/backend/internal/profiles/starter/profile.go), [инфраструктуре](https://github.com/vernal96/go-cms/blob/main/backend/internal/infrastructure/infrastructure.go) и [настройках](https://github.com/vernal96/go-cms/blob/main/backend/internal/settings/settings.go). Подробности о профилях — в [руководстве](../profiles.md); контракты конструкторов и валидации — в [Module declarations](../modules.md).

Во всех примерах ниже меняется **существующий** профиль; не создавайте второй экземпляр `core` или `admin`. `core` обязателен и идёт первым, `admin` также обязателен. Kernel проверяет зависимости модуля, тип database adapter, обязательные привязки и настройки при сборке профиля, в том числе когда к профилю ещё не привязан сайт.

Например, для Mail и Forms в профиле объявите модули в порядке `core`, `admin`, `mail`, `forms`. В приложении дополните существующую `app.Definition`:

```go
definition.MainDatabase.Adapters = append(definition.MainDatabase.Adapters,
    mailpostgres.DatabaseFactory{},
    formspostgres.DatabaseFactory{},
)
definition.ModuleApplications = append(definition.ModuleApplications,
    mail.Application{Transport: yourTransport},
    forms.Application{Providers: []forms.CaptchaProvider{yourProvider}},
)
```

Здесь `mailpostgres` и `formspostgres` — импорты `modules/mail/adapters/postgres` и `modules/forms/adapters/postgres`. `yourTransport` и `yourProvider` создаются приложением и реализуют соответствующие интерфейсы. Физические диски объявляются один раз на уровне приложения, затем модули связывают с ними свои aliases.

| Модуль | Зависимости от модулей | Дополнительные зависимости приложения |
| --- | --- | --- |
| [Core](core/README.md) | Нет | Core database adapter, password hasher, физические хранилища для выбранных cache aliases и дисков |
| [Admin](admin/README.md) | `core` | Использует пользователей и авторизацию Core; собственных adapter/config нет |
| [Mail](mail/README.md) | `core` | Mail database adapter, `mail.Application` с transport, диск `UploadStorage`, лимиты доставки; опционально приватный spool |
| [Forms](forms/README.md) | `core`, `mail` | Forms database adapter, `forms.Application` с CAPTCHA provider, публичные лимиты; опционально приватный spool |
| [Search](search/README.md) | `core` | Search database adapter с `search.Engine` |
| [SEO](seo/README.md) | Нет в `Dependencies()` | SEO database adapter; интеграция с ресурсами Core через extension contract |

## Core

В Starter Core уже подключён. Для нового профиля используйте существующие физические cache stores приложения; `Code` в binding — код store, а `Alias` — локальная потребность модуля:

```go
core.New(core.Config{
    Caches: []cache.Binding{
        {Alias: core.DurableCacheAlias, Code: "shared"},
        {Alias: core.HotCacheAlias, Code: "shared"},
    },
})
```

Импорты: `github.com/vernal96/go-cms-kernel/modules/core` и `github.com/vernal96/go-cms-kernel/cache`. В `MainDatabase.Adapters` нужен `corepostgres.DatabaseFactory{}` из `modules/core/adapters/postgres`. Отдельный `ThumbnailCacheAlias` подключают при использовании thumbnail cache. Остальные настройки `core.Config` (`MediaSettings`, `Images`, TTL, preview/revisions) описывают политику Core; отсутствие binding не создаёт физическое хранилище автоматически. Сведения о домене: [Core](core/README.md).

## Admin

Поставьте после Core:

```go
admin.New()
```

Импорт: `github.com/vernal96/go-cms-kernel/modules/admin`. `admin.New()` не принимает config, database adapter, cache или файловый binding. Runtime получает пользователя и авторизацию из зависимости `core`. В Starter уже подключён; повторять его при добавлении других модулей не нужно. Подробнее: [Admin](admin/README.md).

## Mail

Добавьте после Core; пример задаёт обязательные лимиты и хранение загруженных вложений на объявленном приложением диске `private`:

```go
mail.New(mail.Config{
    MessageIDDomain: "example.test",
    SendMaxAttempts: 3,
    MaxRecipients: 20,
    MaxMessageSize: 10 << 20,
    MaxAttachmentSize: 5 << 20,
    UploadStorage: "private",
})
```

Импорт: `github.com/vernal96/go-cms-kernel/modules/mail`. Добавьте `mailpostgres.DatabaseFactory{}` (`modules/mail/adapters/postgres`) в `MainDatabase.Adapters`, `mail.Application{Transport: yourTransport}` в `ModuleApplications` и физический диск с кодом `private` в `Filesystems`. `yourTransport` реализует `mail.Transport`; выбор SMTP или другого драйвера принадлежит приложению. Если включаете `SpoolEnabled`, задайте `Filesystems: []filesystem.Binding{{Alias: mail.SpoolFilesystemAlias, Code: "private"}}`, `SpoolTTL`, `SpoolCleanupInterval`, `SpoolCleanupBatch`; диск должен быть приватным и поддерживать нужное перечисление для очистки. Нулевой `mail.Config{}` не проходит проверку обязательных лимитов. См. [Mail](mail/README.md) и [шаблоны писем](mail/entities.md).

## Forms

Forms требует уже перечисленные Core и Mail. Пример отключает upload spool; реальные публичные лимиты выбирает приложение с учётом нагрузки:

```go
forms.New(forms.Config{
    ActionMaxAttempts: 3,
    DefaultCaptchaProvider: "captcha-v1",
    Public: forms.PublicLimits{
        MaxRequestSize: 10 << 20,
        MaxScalarFields: 50,
        MaxScalarValueSize: 64 << 10,
        MaxUploadFileSize: 5 << 20,
        MaxUploadCount: 5,
        MaxTotalUploadBytes: 10 << 20,
        SubmissionTimeout: 30 * time.Second,
        RateLimit: 10,
        RateWindow: time.Minute,
        RateEntries: 10000,
    },
})
```

Импорты: `github.com/vernal96/go-cms-kernel/modules/forms` и `time`. Добавьте `formspostgres.DatabaseFactory{}` (`modules/forms/adapters/postgres`) в `MainDatabase.Adapters` и `forms.Application{Providers: []forms.CaptchaProvider{yourProvider}}` в `ModuleApplications`. Код `yourProvider.Code()` должен совпадать с `DefaultCaptchaProvider`; `forms.DevelopmentCaptchaProvider` предназначен только для разработки. `Public` требует положительных лимитов, включая timeout и rate limit. Если включаете upload spool, добавьте `Filesystems` с `forms.SpoolFilesystemAlias` на приватный диск и задайте `SpoolTTL`, `SpoolCleanupInterval`, `SpoolCleanupBatch`. Оба spool aliases независимы, даже если указывают на один физический диск. Подробнее: [Forms](forms/README.md).

## Search

После Core добавьте:

```go
search.New()
```

Импорт: `github.com/vernal96/go-cms-kernel/modules/search`. Добавьте `searchpostgres.DatabaseFactory{}` (`modules/search/adapters/postgres`) в `MainDatabase.Adapters`: он предоставляет требуемый `search.Engine`. Конструктор не принимает config, `ModuleApplications`, cache или файловые bindings. Подробнее: [Search](search/README.md).

## SEO

Добавьте в тот же профиль Core и Admin, затем SEO:

```go
seo.New(seo.Config{
    DefaultTitleTemplate: "{{ resource.title }}",
    MaxTemplateLength: 2000,
    MaxResultLength: 1000,
})
```

Импорт: `github.com/vernal96/go-cms-kernel/modules/seo`. Добавьте `seopostgres.DatabaseFactory{}` (`modules/seo/adapters/postgres`) в `MainDatabase.Adapters`. У SEO нет `Dependencies()` и `ModuleApplications`; его редактор и публичная проекция работают через контракты расширения ресурсов Core. Нулевые максимальные длины получают defaults; неизвестные переменные или некорректные default templates отклоняются при валидации. См. [SEO](seo/README.md) и [SEO-переменные](seo/entities.md).

## Завершение подключения

1. Добавьте модуль в нужные `Profile.Modules` и зарегистрируйте его database factory в приложении. `ModuleApplications` и диски добавляйте там, где они требуются; проверьте порядок и отсутствие дубликатов.
2. Примените миграции для подключённых adapters и запустите приложение. Конкретные команды Starter описаны в [архитектуре запуска](../startup.md).
3. Проверьте доступный API и UI соответствующего модуля по его страницам в [справочнике](README.md). Добавление модуля в профиль меняет возможности только сайтов с этим профилем.
