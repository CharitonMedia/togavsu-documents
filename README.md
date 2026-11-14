# Togavsu Documents and templates

A Togavsu module: the firm's template library, token-filled drafts and whole document
packages on a matter, matter zips, and version notes, compare and copy on top of the
core's file versions. The spec is [docs/SPEC.md](docs/SPEC.md) (from the core repo's
`docs/modules/documents/SPEC.md`), under section 6 of the initial modules PRD.

The module runs in its own container and reaches core records only through the core API,
with its module token and the viewer's context token, so walls, roles and attorney-only
rules come from the core. It keeps its own rows in the `mod_documents` schema through its
own database login and never keeps file bytes: every template body, component body and
generated draft is a core file.

## What it does

- **Templates tab:** templates, components and packages, with search and filters.
  Upload a DOCX, start blank, or save any file you can read as a template. Bodies are
  core library files (CORE-17); Edit body opens them in the core editor.
- **Check template:** every known token, manual token, slot and component problem.
- **New from template** on a matter (and quick-create): pick a template or package, see
  each token's value and source, type the manual ones, generate. A run is all or nothing
  (one `bulkFile` call), never ships an unfilled token, and makes new draft files. Typed
  values are never stored or logged.
- **Drafts tab and widget:** generated drafts by status, Mark final and Mark draft.
- **Download files as zip:** whole matter, folder or selection, status and category
  filters, current or all versions, Markdown copies, folders or flat. The core streams
  the zip from the module's entries (CORE-21); the module keeps a frozen manifest.
- **Version tools:** notes and labels per version, Markdown compare, copy as new file.
- **Attorney profiles** (admin): bar number, bar state, courtesy title, signature line.
- **Release** (admin): drop the module's hold on a trashed file so the core can purge it.

Every action is a JSON endpoint under `/m/documents/api/`, listed in
`/m/documents/api/openapi.json`; the pages' forms post to the same endpoints.

## Layout

```
cmd/documents/        serve and package subcommands
documents.go          ID, embedded package files, Packager adapter, Config, Handler
api.go                core API client
docx.go               DOCX read, token fill, slot copy, part checks, blank DOCX
tokens.go             vocabulary and resolver
generate.go           assemble, runs, packages, PDFs, timeline event
library.go            templates, components, packages, Check template
drafts.go             drafts tab, widget, status writes
zip.go                entries, manifest, archive streaming
versions.go           notes, compare, copy
attorneys.go          attorney profiles
events.go             webhook, Release
routes.go, pages.go   route table, OpenAPI, page handlers
pages/                html/template pages, one stylesheet, one script
package/              togavsu-module.yaml and migrations 0001 to 0006
internal/fakecore/    in-memory core for tests
internal/modkit/      context and webhook headers, webhook signatures
```

## Build and test

```
go vet ./...
TOGAVSU_TEST_DATABASE_URL=postgres://postgres:pg@127.0.0.1:5432/postgres?sslmode=disable go test ./...
docker build -t togavsu-documents .
```

The Postgres tests create and use their own database, `togavsu_documents`, with a
skeleton `core` schema beside `mod_documents`, and run the module against the fake core
in `internal/fakecore`. Without `TOGAVSU_TEST_DATABASE_URL` they skip; the unit tests
still run.

## Run

```
documents serve -core http://core:8080 -secrets /run/secrets -db-host postgres:5432 -db-name togavsu
```

Secrets come from the Docker secrets the core hands the supervisor: `togavsu_api_token`,
`togavsu_webhook_secret`, `togavsu_db_role`, `togavsu_db_password` and
`togavsu_db_schema` (default `mod_documents`).

## Package and sign

`documents package -key <file> -out documents.signed.json` goes through the `Packager`
adapter in `documents.go`. The core verifies packages with its own internal code, which
a separate repository can't import and shouldn't copy, so the default adapter is a stub
that refuses until the core publishes a public packaging and signing package or CLI.
Wire that in as `documents.DefaultPackager` when it exists, and set the image digest in
`package/togavsu-module.yaml` (a placeholder today).

## License

Same as Togavsu: see [LICENSE](LICENSE).
