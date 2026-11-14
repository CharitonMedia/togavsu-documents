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
documents.go          ID, embedded package files, Config, Handler
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
internal/pending/     the manifest the pinned core installs today (tests only)
acceptance/           acceptance suite on the real core (pkg/modulekit/coretest)
```

## Build and test

```
go vet ./...
TOGAVSU_TEST_DATABASE_URL=postgres://postgres:pg@127.0.0.1:5432/postgres?sslmode=disable go test -race ./...
docker build -t togavsu-documents .
```

`go.mod` pins the core for the public module kit and adds no dependency the core
doesn't pin. Two Postgres suites run on a superuser `TOGAVSU_TEST_DATABASE_URL`; outside
CI they skip without it, and in CI they fail without it.

- `acceptance/` installs the signed package on the real composed core
  (`pkg/modulekit/coretest`), approves it, serves the module in process with the
  deployment's token, webhook secret and database login, and drives every MOD-DOC
  criterion. A criterion that needs a CORE item the pinned core lacks runs what it can
  and skips naming the item; the skip is decided by asking the running core, so it lifts
  when the pin moves to a core that has the item.
- The root package's `*_pg_test.go` run the module against the fake core in
  `internal/fakecore`, in their own database `togavsu_documents`.

The shipped manifest carries the CORE-08 sidebar fields (section, icon, order) the spec
asks for, which the pinned core's install checks refuse, so `togavsu-module check
package` and `documents package` refuse it until CORE-08 lands. `internal/pending` lists
those fields in one place; the tests check, sign and install the package without them,
and `TestPackageCheck` fails as soon as the core accepts one. The source passes the
core's settings check (`modulekit.CheckSource`, in `TestCheckSource`): small fixed
numbers are spelled as arithmetic in `fixed.go`, as the core's matter-clock does, and
tunables are module settings.

## Run

```
documents serve -core http://core:8080 -secrets /run/secrets -db-host postgres:5432 -db-name togavsu
```

Secrets come from the Docker secrets the core hands the supervisor: `togavsu_api_token`,
`togavsu_webhook_secret`, `togavsu_db_role`, `togavsu_db_password` and
`togavsu_db_schema` (default `mod_documents`).

## Package and sign

`documents package -key <file> -out documents.signed.json` runs the core's install
checks through `pkg/modulekit`, archives the package, signs it with the base64 Ed25519
seed in the key file and prints the PEM public key the admin trusts. It never makes or
keeps a key. The image digest in `package/togavsu-module.yaml` is a placeholder the
release workflow pins.

## License

Same as Togavsu: see [LICENSE](LICENSE).
