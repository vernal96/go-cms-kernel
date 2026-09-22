# Go CMS Kernel

Reusable Go kernel, built-in modules, persistence adapters, and infrastructure
connectors for [Go CMS](https://github.com/vernal96/go-cms).

## Installation

```bash
go get github.com/vernal96/go-cms-kernel@v0.1.0
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

Releases use semantic Go module tags. Consumers should depend on a fixed tag;
the project intentionally does not require a `replace` directive or `go.work`.

The PostgreSQL connector uses a one-hour connection lifetime when
`Config.ConnMaxLifetime` is zero. Set a positive duration to override it;
negative durations are rejected. This applies to both the pgx pool and
migration connections.
