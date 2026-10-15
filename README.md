# Go CMS Kernel

Reusable Go kernel, built-in modules, persistence adapters, and infrastructure
connectors for [Go CMS](https://github.com/vernal96/go-cms).

[Документация GO CMS](docs/README.md): запуск Starter, профили, подключение модулей и разработка шаблонов.

## Installation

```bash
go get github.com/vernal96/go-cms-kernel@v0.4.0
```

The kernel package lives at the module root. Common packages include:

```go
import (
	"github.com/vernal96/go-cms-kernel"
	"github.com/vernal96/go-cms-kernel/app"
	"github.com/vernal96/go-cms-kernel/connectors/postgres"
	"github.com/vernal96/go-cms-kernel/modules/core"
)
```

## Repository layout

- the module root owns generic runtime and profile contracts;
- `app`, `transport`, `cache`, `filesystem`, and related packages own reusable
  application mechanics;
- `modules` contains the built-in CMS modules and their public adapters;
- `connectors` contains reusable infrastructure implementations.

Project-specific profiles, configuration, bindings, and executable entrypoints
remain in the consuming application.

## HTTP API

Current source mounts all CMS APIs below `/api`. Public routes include
`/api/site`, `/api/menu`, `/api/search`, `/api/forms/{code}` and
`/api/_cms/runtime`. Management endpoints keep their existing `/api/...` URLs.
The starter health probe `/healthz` and file delivery `/_cms/files/...` remain
outside the API prefix, including signed file URLs.

A resource at `/about` is read through `/api/resources/resolve/about`;
`/api/resources/resolve` resolves the root resource. The resolver accepts `GET`
and `HEAD`; other methods return `405 Method Not Allowed` with `Allow: GET, HEAD`.
Stored resource paths, menu/search URLs and SEO canonical URLs remain website
paths. Internal ResourceLink redirects point to the target's API URL; external
Link redirects keep their configured URL. Former resource URLs such as `/api`,
`/api/`, `/api/about` and `/api/news/article` return 404, as do other unmatched
routes. Registered API endpoints such as `/api/site` and `/api/menu` remain
available.

Successful page, library, and library item JSON responses include
`resource.breadcrumbs`, an array of `{title, link}`
objects ordered from published ancestors to the current resource. A title uses
`menu_title` when set and otherwise `title`; links are public page paths.
Unpublished and deleted resources are skipped. Profile setting
`Settings.IncludeHomeInBreadcrumbs` optionally prepends the published `/` home
resource without duplicating it when it is already an ancestor. See the [HTTP API
reference](docs/http-api.md#хлебные-крошки-ресурса) and [profile guide](docs/profiles.md#хлебные-крошки).

Module `Registrar` routes and mounts remain relative to `/api`. The public
handler receives the path after stripping that prefix once; generated API links
use `httptransport.APIPrefix`. API-relative namespaces `/auth`, `/admin`,
`/sites`, `/site-profiles`, `/files`, `/media`, `/administration`, `/_cms`,
`/api` and `/resources` are reserved by the platform and cannot be claimed by
literal module routes or mounts. Unknown management URLs never fall through to
public resources.

The `/api` routing contract is included in v0.4.0. Update consumers to the new
paths; old unprefixed routes are not retained.

Field validator definitions, built-ins, module contributions and admin metadata are documented in [Field validation](docs/field-validation.md).

## Module declarations

Current source uses typed constructors and `[]kernel.Module`; see [Module declarations](docs/modules.md), [built-in module setup](docs/modules/adding-modules.md), [page templates](docs/templates.md) and the [counter example](examples/counter/counter.go). This API is included in v0.4.0; update custom modules to implement `Validate` and use typed constructors.

## Development

Use the Go version declared in `go.mod` and run:

```bash
go test ./...
go vet ./...
go build ./...
go mod tidy -diff
```

Integration tests requiring PostgreSQL, Kafka, Redis, or S3
skip when their documented environment variables are not configured.

The PostgreSQL read-optimization tests also check statement counts when
`CMS_TEST_POSTGRES_QUERY_COUNTS=1`. Use an isolated test database with
`pg_stat_statements` preloaded and its extension installed, set the usual
`CMS_TEST_POSTGRES_*` connection variables, and run without other database workloads:

```bash
CMS_TEST_POSTGRES_QUERY_COUNTS=1 go test -p 1 ./modules/core/adapters/postgres \
  -run 'TestPostgres(Allowed|LibraryPage)' -count=1 -v
```

These tests cover batch authorization, the admin session endpoint, immediate
permission changes, cross-site access, and LibraryItem versions and pagination.
Without the query-count flag they still verify behavior but do not measure SQL calls.

Releases use semantic Go module tags. Consumers should depend on a fixed tag;
the project intentionally does not require a `replace` directive or `go.work`.

The PostgreSQL connector uses a one-hour connection lifetime when
`Config.ConnMaxLifetime` is zero. Set a positive duration to override it;
negative durations are rejected. This applies to both the pgx pool and
migration connections.
