# pdbcore

Shared engine for [pdbq](https://github.com/suprbdev/pdbq) (GraphQL) and
[pdbr](https://github.com/suprbdev/pdbr) (REST): everything about turning a
live PostgreSQL database into an API that is not specific to one transport.
Library only — no binary, no GraphQL or REST grammar, no `gqlparser`
(CI asserts `go list -deps ./...` contains no GraphQL dependency).

The products own the transport: schema/route generation, request parsing,
SQL compilation, response shaping. pdbcore owns what both need identically:
the catalog model and its introspection, naming helpers, the schema cache and
watcher, PostGIS value handling, authentication, transaction execution with
RLS identity, JWT minting, HTTP scaffolding, configuration loading, the
generic plugin registry, the catalog half of smart comments, and the one test
fixture both products are tested against.

## Packages

| Package | What it holds |
|---|---|
| `introspect` | `Catalog` model (`Table`, `Column`, `Constraint`, `ForeignKey`, `Index`, `Enum`, `Composite`, `Function`, privileges, RLS flag, comments), `Introspect(ctx, querier, schemas)` (pg_catalog reader incl. `@enum` table conversion), `Diff` (human-readable drift lines), `Hash`, `CatalogFormatVersion` |
| `smarttags` | PostGraphile-style smart-comment parser: `Parse`, `Strip`, `Tags`, `Omits`, `@behavior`, list splitting |
| `inflect` | naming pipeline types (`Kind`, `Input`, `Next`) + string helpers (`UpperCamel`, `LowerCamel`, `Singularize`, `Pluralize`, `EnumValue`, `ByColumns`); products define their own `Kind` constants and `Default` |
| `cache` | gzip+JSON catalog cache: `Save` / `Load` / `Check`, format-version and content-hash checks, 256 MiB decompression guard |
| `watch` | `Watcher`: DDL event trigger + `LISTEN/NOTIFY` (`TriggerName`, `Channel`), hash-polling fallback when the trigger cannot be installed → `OnChange(*Catalog)` |
| `postgis` | spatial value layer: flavour detection, GeoJSON/WKT/EWKT parsing to one bound parameter, filter `Ops`, derived accessors, transform/simplify/KNN/distance SQL |
| `auth` | `Authenticator`: JWT (HS*, JWKS RS*/ES* with kid-matched cache), trusted-gateway claim headers with `TrustedProxies`, role resolution and allowlist; `ErrInvalidToken` / `ErrRoleNotAllowed` |
| `pgexec` | pool `Connect`, `RunTx` (isolation, `SET LOCAL ROLE`, claims via `set_config(prefix.k, v, true)`, 40001/40P01 retries), `Identity`, `Classify` PG errors into classes with `dev`/`prod`/`strict` message policies, `QuoteIdent`/`ValidRole`/`ValidClaimKey` |
| `jwtmint` | composite-typed function results → signed HS256 JWT (`MintsFunction`, `Mint`, `Sign`) |
| `httpx` | CORS + preflight, gzip, `Healthz`, `ListenAndServe` with the standard timeouts and graceful shutdown |
| `confx` | koanf `Load` (defaults < YAML < env < changed flags), env key mapper (`_` → `.`, `__` → `_`, comma-separated lists), `ExampleYAML` from `koanf`/`doc` tags, `Discover`, `ParseTrustedProxy`, shared config sections (`Database`, `Schema`, `Filters`, `RLS`/`Auth`, `TX`, `Watch`, `Errors`, `Plugins`, `Log`) + `Validate*` helpers, `AuthConfig(RLS)` |
| `plugin` | `Plugin`, `Registry` (priority + registration order), `CatalogHook`, `InflectionHook`, `TransformCatalog`, `Inflector` chain |
| `smartcomments/catalog` | catalog half of the smart-comments plugin (`@omit`, `@primaryKey`, `@unique`, `@foreignKey`, `@notNull`, `@filterable`, `@name` index) — products add their transport half on top |
| `testutil` | `FixtureSQL` (embedded DDL) and `FixtureCatalog()`, the one fixture both products test against; `testutil/cmd/fixture` prints the SQL; `drift_test.go` is the fixture drift gate |

Every package has a package comment describing its contract; `go doc
github.com/suprbdev/pdbcore/<pkg>` is the reference.

## How products consume it

```go
import (
	"github.com/suprbdev/pdbcore/cache"
	"github.com/suprbdev/pdbcore/introspect"
	"github.com/suprbdev/pdbcore/pgexec"
)

cat, _ := introspect.Introspect(ctx, pool, cfg.Schema.Schemas) // or cache.Load(path)
// ... the product builds its schema / routes from cat and runs its own compiler ...
err := pgexec.RunTx(ctx, pool,
	pgexec.TxOptions{Isolation: iso, MaxRetries: 2, RLS: true},
	pgexec.Identity{Role: role, Claims: claims, ClaimsPrefix: "pdbq.claims"},
	func(q pgexec.Querier) error { return q.QueryRow(ctx, sql, args...).Scan(&data) })
```

Both products embed the `confx` sections in their own `Config` struct (the
product owns `Server` and its defaults such as `watch.channel` and the claim
header prefix), wrap `plugin.Registry` with their transport-specific hooks,
and register `smartcomments/catalog.New()` inside their smart-comments
plugin. `rls.claims_prefix` defaults to `pdbq.claims` in both so RLS policies
work unchanged behind either server; `watch.TriggerName` differs
(`pdbq_watch` / `pdbr_watch`) so both can watch one database.

Until the first tag lands the products point at a sibling checkout:

```
// go.mod (pdbq, pdbr)
replace github.com/suprbdev/pdbcore => ../pdbcore
```

## The fixture and the drift gate

`testutil.FixtureSQL` (`testutil/fixture.sql`) and `testutil.FixtureCatalog()`
(`testutil/catalog.go`) describe the same schema: users/posts/comments with
RLS policies on `posts`, `places`/`events` (PostGIS), a `metrics` view,
enums, a composite `jwt` type, and the functions the products' RPC /
computed-column tests use. They **must** stay in sync:
`testutil/drift_test.go` (`TestFixtureCatalogMatchesLiveIntrospection`) loads
`FixtureSQL` into a database, introspects it and asserts the result equals
`FixtureCatalog()` (needs `PDBCORE_TEST_DATABASE_URL`; `make test-e2e`
provides one). Products regenerate `db/init/01-schema.sql` from it (their
`make fixture` runs `go run github.com/suprbdev/pdbcore/testutil/cmd/fixture`)
and run their goldens against `FixtureCatalog()`, so a fixture change is a
pdbcore change first and a golden update in both products second.

## Versioning

Semver `v0.x.y` while pre-1.0: minor bumps may break API, patch bumps may
not; products pin an exact version. `introspect.CatalogFormatVersion` is
owned here — bumping it is a minor release and both products upgrade in
lockstep so a cache written by one (`pdbq schema dump` / `pdbr schema dump`)
loads in the other. Fixture changes are patch/minor releases; products
regenerate their goldens on upgrade.

## Development

```sh
make test       # hermetic unit tests (go test -race ./...)
make test-e2e   # DB-backed tests (fixture drift gate, pgexec RLS matrix, watch) on a disposable PostGIS (project pdbcore-test, host port 5434)
make lint       # golangci-lint (falls back to go vet)
make fixture    # print testutil.FixtureSQL
make release    # interactive: clean tree check, version prompt, test+lint, annotated tag, push
```

CI (`.github/workflows/ci.yaml`) runs vet, golangci-lint, the race tests and
the no-GraphQL-dependency check, plus the DB-backed suites against a PostGIS
service.

## Release flow

pdbcore is a library, so a release is a tag: `make release` verifies the tree
is clean, prompts for `vX.Y.Z`, runs `make test lint`, tags and pushes; the
`Release` workflow (`.github/workflows/release.yaml`) re-runs the race tests
and creates the GitHub release with generated notes (no binaries; publishing
is skipped under `act`).

Consumers then bump with `go get github.com/suprbdev/pdbcore@vX.Y.Z` (and,
for the very first tag, delete their `replace ../pdbcore` line and any
"check out pdbcore next to the repo" / `go mod vendor` steps in their
Makefile, Dockerfile and workflows). Order for the first release: pdbcore
`v0.1.0` → pdbq `v0.11.0` → pdbr `v0.1.0`; the exact steps are in pdbr's
`docs/deployment.md` § Releasing.
