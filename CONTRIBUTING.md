# Contributing to Goi

Goi is deliberately small: Go, server-rendered HTML, SQLite, and plain browser
JavaScript. A focused change that fits the existing code is usually better than
a new layer or framework.

By contributing, you agree to license your work under the project's
[Sustainable Use License](LICENSE).

## Getting started

You need Go 1.26.6, Node.js for JavaScript tests, and Chrome or Chromium for
extension work. There is no npm install and no frontend build step.

Run the server from the repository root:

```sh
APP_DATA_DIR="$PWD/data" APP_AUTH_MODE=false go run ./cmd/server
```

Then open [http://127.0.0.1:8080](http://127.0.0.1:8080). Templates, static
files, migrations, and extension files are embedded in the Go application.

For UI work, use disposable data instead of your own study database:

```sh
go run ./cmd/devdata qa
APP_DATA_DIR="$PWD/data/test" APP_AUTH_MODE=false go run ./cmd/server
```

`devdata` replaces `data/test`, so stop any server using that directory first.
The `lessons`, `reviews`, and `mixed` scenarios are useful when you want a
smaller fixture. Run `go run ./cmd/devdata list` to inspect the available data
and commands.

## Finding your way around

- `cmd/server` starts the web app and background work.
- `cmd/backup` handles application-aware backup and offline restore.
- `cmd/devdata` creates disposable development data.
- `internal/<domain>` holds handlers, storage, and domain rules.
- `internal/database` owns SQLite, migrations, locking, and low-level
  backup/restore.
- `web/templates` and `web/static` contain the progressively enhanced web UI.
- `browser-extension` is the unpacked Manifest V3 extension.

Go code, migrations, and executable commands are the source of truth. The
[README](README.md) covers normal installation and configuration.

## Making a change

Read the relevant handler, store, and tests before editing. Then make the
smallest change that completes the user-visible path.

- Keep business rules out of templates and middleware.
- Keep database queries explicit and use a transaction when writes belong
  together.
- Preserve ordinary forms and links when JavaScript enhances a page.
- Keep keyboard access, visible focus, reduced motion, and useful errors.
- Prefer the standard library and existing project code over a new dependency.
- Remove temporary logging, dead code, stale comments, and debug fixtures.

For a database change, leave `internal/database/migrations/00001_init.sql`
alone. Add the next numbered migration and update `schemaVersion` in
`internal/database/migrations.go`. Open test databases through
`internal/database` and assert the durable result.

For extension work, edit `browser-extension` directly and reload it from
`chrome://extensions`. Keep the bearer token inside the service worker, build
API URLs from the configured Goi origin, and request only the permissions the
feature needs.

Update `NOTICE` if a linked dependency or embedded dataset changes its license
requirements.

## Before you finish

Run focused tests while you work and the full relevant suite before handing off
the change. [TESTING.md](TESTING.md) contains the canonical commands and manual
browser checks.

Finally, read the diff as prose: names should be clear, errors useful,
documentation accurate, and unrelated files untouched.
