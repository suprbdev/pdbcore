# pdbcore

Shared engine for [pdbq](https://github.com/suprbdev/pdbq) (GraphQL) and
[pdbr](https://github.com/suprbdev/pdbr) (REST): everything about turning a
live PostgreSQL database into an API that is not specific to one transport.
Library only — no binary, no GraphQL or REST grammar, no `gqlparser`
(CI asserts `go list -deps ./...` contains no GraphQL dependency).

The products own the transport: schema/route generation, request parsing,
SQL compilation, response shaping. pdbcore owns what both need identically:
the catalog model and its introspection, naming helpers, the schema cache and
watcher, PostGIS and contrib-type value handling, authentication, transaction execution with
RLS identity, JWT minting, HTTP scaffolding, Prometheus metrics, configuration loading, the
generic plugin registry, the catalog half of smart comments, and the one test
fixture both products are tested against.

## Packages

| Package | What it holds |
|---|---|
| `introspect` | `Catalog` model (`Table`, `Column`, `Constraint`, `ForeignKey`, `Index`, `Enum`, `Composite`, `Function`, `Extension`, privileges, RLS flag, comments; `Column.Spatial` (`SpatialType`: PostGIS subtype, SRID, Z/M from the geometry/geography typmod) and `Column.SQLType` (built-in modifiers such as `character varying(255)`, `numeric(10,2)`); `Catalog.ServerVersionNum` for version gating; `Column.GeneratedKind` (identity, stored, virtual); domains (`Catalog.Domains`: base type, parent domain, NOT NULL, CHECK constraints, default, comment; `Column.Domain` / `FuncArg.Domain` / `Function.ReturnDomain` refs, `Catalog.Domain`, `DomainByRef`, `DomainChain`); PostgreSQL 18 temporal keys (`Constraint.WithoutOverlaps`, `ForeignKey.Period` with `JoinPairs`, `Table.RowKeys`) and `ForeignKey.NotEnforced`), `Introspect(ctx, querier, schemas)` (pg_catalog reader incl. `@enum` table conversion and installed extensions), `HasExtension`, polymorphic signatures (`IsPolymorphic`, `Function.PolymorphicFamilies`, `Catalog.ResolvePolymorphic`, `Catalog.ParseTypeName` for SQL type spellings), `Clone` (deep copy for per-contract catalog hooks), `Diff` (human-readable drift lines), `Hash`, `CatalogFormatVersion` |
| `smarttags` | PostGraphile-style smart-comment parser: `Parse`, `Strip`, `Tags`, `Omits`, `@behavior`, `Contracts` (`@contract` / `@behavior ±contract:<name>` membership), list splitting |
| `contract` | schema contracts (see [Schema contracts](#schema-contracts)): `Contract{Name, Default}` with the `Include`/`Exclude` policies, `View` (clone + filter), `Apply`, `OmitAll`; also a `CatalogHook` |
| `inflect` | naming pipeline types (`Kind`, `Input`, `Next`) + string helpers (`UpperCamel`, `LowerCamel`, `Singularize`, `Pluralize`, `EnumValue`, `ByColumns`); products define their own `Kind` constants and `Default` |
| `cache` | gzip+JSON catalog cache: `Save` / `Load` / `Check`, format-version and content-hash checks, 256 MiB decompression guard |
| `watch` | `Watcher`: DDL event trigger + `LISTEN/NOTIFY` (`TriggerName`, `Channel`), hash-polling fallback when the trigger cannot be installed → `OnChange(*Catalog)` |
| `postgis` | spatial value layer: flavour detection, GeoJSON/WKT/EWKT parsing to one bound parameter, filter `Ops`, derived accessors, transform/simplify/KNN/distance SQL |
| `contrib` | operator vocabularies + value coercion for full-text search, `pg_trgm`, `hstore`, `ltree`, `citext`, range/multirange and `inet`/`cidr` columns (see [Contrib vocabularies](#contrib-vocabularies)): `ForColumn`/`ForType` lookup bound to the extension's schema, filter `Op`s, ordering `Expr`s, `Accessor`s, value `Codec`s |
| `auth` | `Authenticator`: JWT (HS*, JWKS RS*/ES* with a kid-matched cache refreshed by one shared background fetch, stale keys served for one extra TTL during endpoint outages, failure backoff, endpoint details logged and never returned to clients), trusted-gateway claim headers with `TrustedProxies` (one spelling per claim: `User-Id` is `user_id`; underscores, repeats and case-folded duplicates are rejected; optional `HeaderClaims` allowlist; the gateway must strip every client-supplied prefixed header), role resolution and allowlist; `ErrInvalidToken` / `ErrRoleNotAllowed` |
| `pgexec` | pool `Connect`, `RunTx` (isolation, `SET LOCAL ROLE`, claims via `set_config(prefix.k, v, true)`, 40001/40P01 retries), `QueryTx` (one pipelined round trip), `Observer` (per-attempt duration and error via `TxOptions.Observer`, `Observe` for direct statements), `Identity`, `Classify` PG errors into classes with `dev`/`prod`/`strict` message policies (malformed tsquery/ltree operands and unknown text search configs are user input), `QuoteIdent`/`ValidRole`/`ValidClaimKey` |
| `jwtmint` | composite-typed function results → signed HS256 JWT (`MintsFunction`, `Mint`, `Sign`) |
| `httpx` | CORS + preflight, gzip, `Healthz`, `ListenAndServe` with the standard timeouts and graceful shutdown |
| `metrics` | Prometheus instrumentation on a per-instance registry (or a caller's `Registerer` + `ConstLabels`): HTTP `Middleware` (requests, duration, in flight by route class, method, status class; `SetRoute`), `DBObserver` (a `pgexec.Observer`: duration, errors by SQLSTATE class, retries), `RegisterPool` (pgxpool `Stat`), `SchemaRebuilt`, `ObserveCompile`, product counters via `NewCounter`; `NewRegistry` + `RegisterPoolOn` for several instances (one per contract) on one registry; a nil `*Metrics` records nothing (see [Metrics](#metrics)) |
| `confx` | koanf `Load` (defaults < YAML < env < changed flags), env key mapper (`_` → `.`, `__` → `_`, comma-separated lists), `ExampleYAML` from `koanf`/`doc` tags, `Discover`, `ParseTrustedProxy`, shared config sections (`Database`, `Schema`, `Filters`, `RLS`/`Auth`, `TX`, `Watch`, `Errors`, `Plugins`, `Log`, `Metrics`, `Contract` list entries) + `Validate*` helpers (`ValidateContracts`), `AuthConfig(RLS)` |
| `plugin` | `Plugin`, `Registry` (priority + registration order), `CatalogHook`, `InflectionHook`, `TransformCatalog`, `Inflector` chain |
| `smartcomments/catalog` | catalog half of the smart-comments plugin (`@omit`, `@primaryKey`, `@unique`, `@foreignKey`, `@notNull`, `@filterable`, `@anyelement` / `@anycompatible` on polymorphic functions, `@name` index) — products add their transport half on top |
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

## Metrics

`metrics.New(metrics.Options{Namespace: "pdbq"})` builds one instance's
instruments on a private registry with the Go and process collectors, so two
products embedded in one process never collide; pass `Registerer` (and
`Gatherer` when it is not a `*prometheus.Registry`) to share an existing one,
with `ConstLabels` telling instances apart. Products wire it like this:

```go
m, _ := metrics.New(metrics.Options{Namespace: "pdbr"}) // nil when metrics.enabled is false
_ = m.RegisterPool(pool)
opts := pgexec.TxOptions{Observer: m.DBObserver()} // nil observer on a nil *Metrics
handler = m.Middleware(handler, classify)          // classify: *http.Request -> route class
mux.Handle(cfg.Metrics.Path, m.Handler())          // or a separate listener on cfg.Metrics.Addr
watcher.OnChange = func(cat *introspect.Catalog) { m.SchemaRebuilt(rebuild(cat)) }
hits, _ := m.NewCounter("document_cache_requests_total", "Document cache lookups.", "result")
```

| Metric (`<ns>_` prefix) | Type | Labels |
|---|---|---|
| `http_requests_total`, `http_request_duration_seconds` | counter, histogram | `route`, `method`, `status_class` |
| `http_requests_in_flight` | gauge | |
| `db_duration_seconds` | histogram | `op` (`run_tx`, `query_tx`, or the caller's name for `pgexec.Observe`) |
| `db_errors_total` | counter | `op`, `sqlstate_class` (`23`, `40`, ..., `canceled`, `timeout`, `other`) |
| `db_retries_total` | counter | `op` |
| `db_pool_{acquired,idle,constructing,total,max}_conns` | gauge | |
| `db_pool_{acquires,canceled_acquires,empty_acquires,new_conns,max_lifetime_destroys,max_idle_destroys}_total`, `db_pool_acquire_duration_seconds_total`, `db_pool_empty_acquire_wait_seconds_total` | counter | |
| `schema_rebuilds_total` | counter | `result` (`success`, `failure`) |
| `schema_last_rebuild_timestamp_seconds` | gauge | |
| `compile_duration_seconds` | histogram | |

`confx.Metrics` (`metrics.enabled`, default off; `metrics.path`, default
`/metrics`; `metrics.addr`, an optional separate listener) and
`confx.ValidateMetrics` are the shared config section.

## Schema contracts

A contract is a named view of the catalog for one audience (`public`,
`partner`, `internal`): the products build one API per configured contract
from the same introspected catalog and serve each under its own path. The
catalog half lives here so both products read the same tags the same way:

```sql
COMMENT ON TABLE app.audit_log IS '@contract internal';           -- only in internal
COMMENT ON COLUMN app.users.email IS '@behavior -contract:public'; -- everywhere but public
COMMENT ON FUNCTION app.feed() IS '@behavior +contract:partner';   -- into a default-exclude partner contract
```

```go
ct := contract.Contract{Name: "partner", Default: contract.Exclude}
view, sum := ct.View(cat) // deep copy, filtered; cat is untouched
// ... run the plugin catalog hooks and the normal API build on view ...
```

- `-contract:<name>` hides; `@contract a, b` and `+contract:<name>` include;
  a `@contract` list naming other contracts hides; otherwise tables and
  functions follow the contract's `Default` policy (`include` or `exclude`)
  and columns, foreign keys and partitions follow their table.
- A contract only removes: hidden tables, functions and foreign keys leave
  the copy (with every foreign key, function and row-typed column
  referencing a hidden table), and a hidden column gets a leading bare
  `@omit` line so the products' existing `@omit` handling removes it from
  every surface. The products therefore require the `smart-comments`
  plugin when contracts are configured.
- Row-level security is unchanged: a contract narrows the API, PostgreSQL
  still decides which rows a role sees.
- `confx.Contract` is the config entry (`name`, `default`, `path`;
  `contracts: [public, partner]` and `PDBQ_CONTRACTS=public,partner` give
  names only) and `confx.ValidateContracts` its checks (names, policies,
  unique non-overlapping paths).
- Per-contract metrics: one `metrics.Metrics` per contract on a shared
  `metrics.NewRegistry()` with `ConstLabels{"contract": name}`, the shared
  pool registered once with `metrics.RegisterPoolOn`.

## Contrib vocabularies

`contrib.ForColumn(cat, col)` returns the vocabularies that apply to a column
(by its resolved base type, so domains work; extension types only when the
catalog records the extension). Each is bound to the extension's schema from
`Catalog.Extensions`, so types, functions and operators render
schema-qualified (`"ext".nlevel(c)`, `(c) OPERATOR("ext".@>) $1::"ext".ltree`)
and work when that schema is not on the `search_path`. Every `Op`/`Expr`
renders from the column expression and placeholders only — values travel as
bind parameters (`op.Params(v)` coerces the operand), so compiled SQL stays a
pure function of the query shape. Infix operands are parenthesized, so a
non-atomic column expression groups the same bound or unbound.

| Vocabulary | Column types | Operators | Exprs / accessors | Codec | Index |
|---|---|---|---|---|---|
| `fulltext` | `tsvector` | `matches` (`to_tsquery`, PostgREST `fts`), `matchesPlain` (`plainto_tsquery`, `plfts`), `matchesPhrase` (`phraseto_tsquery`, `phfts`), `matchesWebsearch` (`websearch_to_tsquery`, `wfts`); operand `"query"` or `{query, config}` (`$n::regconfig`); `Op.RankSQL` → `ts_rank` / `ts_rank_cd` | — | text ↔ `$1::tsvector` | GIN, GiST |
| `textsearch` | `text`, `varchar`, `bpchar`, `citext` | same four, over `to_tsvector([config,] col)` | — | — | GIN expression index on `to_tsvector('cfg', col)`, only when queries pass that config (the config-less form is not indexable); prefer a generated `tsvector` column |
| `trgm` (`pg_trgm`) | text-like | `similarTo` (`%`), `wordSimilarTo` (`<%`), `strictWordSimilarTo` (`<<%`) | `similarity`, `wordSimilarity`, `strictWordSimilarity`; `distance` (`<->`), `wordDistance` (`<<->`), `strictWordDistance` (`<<<->`) for ordering | — | GIN/GiST `gin_trgm_ops`/`gist_trgm_ops`; KNN ordering GiST only; none on `bpchar` (only an expression index on `col::text`). Word ops need pg_trgm 1.2, strict ones 1.4 (`Vocabulary.ForVersion`, applied by `ForColumn` from `Extension.Version`) |
| `citext` | `citext` | — (text vocabularies apply) | — | `$1::"ext".citext`; use `Vocabulary.Operator("=")` — unqualified or text-typed comparisons fall back to case-sensitive `text` operators | — |
| `hstore` | `hstore` | `hasKey` (`?`), `hasAllKeys` (`?&`), `hasAnyKeys` (`?\|`), `contains` (`@>`), `containedBy` (`<@`) | — | JSON object of string → string\|null ↔ `hstore($1::text[])` / `hstore_to_jsonb` | GIN `gin_hstore_ops`, GiST `gist_hstore_ops` (not `<@`) |
| `ltree` | `ltree` | `ancestorOf` (`@>`), `descendantOf` (`<@`), `ancestorOfAny` / `descendantOfAny` (`ltree[]` operand), `matchesLquery` (`~`), `matchesAnyLquery` (`?` `lquery[]`), `matchesLtxtquery` (`@`) | `nlevel` | text ↔ `$1::"ext".ltree` | GiST `gist_ltree_ops` (not the `*Any` forms) |
| `ltree[]` | `ltree[]` | same names, "some element …" | — | list ↔ `$1::"ext".ltree[]` | GiST `gist__ltree_ops` (not `ancestorOf`) |
| `range` | `int4range`, `int8range`, `numrange`, `tsrange`, `tstzrange`, `daterange` | `overlaps` (`&&`), `contains` (`@>`), `containsElement` (`@>` element), `containedBy` (`<@`), `strictlyLeftOf` (`<<`), `strictlyRightOf` (`>>`), `notExtendsRightOf` (`&<`), `notExtendsLeftOf` (`&>`), `adjacentTo` (`-\|-`) | `start`, `end`, `startInclusive`, `endInclusive`, `startInfinite`, `endInfinite`, `isEmpty` | `{start, end, startInclusive, endInclusive, isEmpty}` or a literal (`"[1,5)"`) ↔ `int4range($1::int4, $2::int4, $3::text)` / jsonb object | GiST, SP-GiST |
| `multirange` | the six built-in multiranges (PG 14+) | same, with a range operand | same | list of ranges or a literal (`"{[1,3), [5,7)}"`) ↔ `int4multirange(VARIADIC ARRAY(… unnest($1, $2, $3) …))` / jsonb array | GiST |
| `inet` | `inet`, `cidr` | `containedBy` (`<<`), `containedByOrEquals` (`<<=`), `contains` (`>>`), `containsOrEquals` (`>>=`), `containsOrContainedBy` (`&&`) | `family`, `masklen`, `host`, `network` | text ↔ `$1::inet` / `$1::cidr` | GiST `inet_ops`, SP-GiST |

Output SQL comes in two flavours. `OutputSQL` (`postgis.OutputSQL`, `Derived.SQL`, `Codec.OutputSQL`, `RangeOutputSQL`, `MultirangeOutputSQL`, `HstoreOutputSQL`) yields `jsonb`; the `OutputJSONSQL`/`JSONSQL` counterparts yield `json`, which skips jsonb construction and is markedly cheaper. Use json when the value is only embedded in a `json_build_object`/`json_agg` response; keep jsonb when it is compared, grouped, deduplicated or aggregated with jsonb functions.

Each `Op` also carries `Arg` (operand kind), `Description`, `Index`
(`IndexHint{Method, OpClass, Note, Ordering}`) and `Extension`; accessor and
expression types are pg type names (`int4`, `float4`, the range's element
type). Custom (user-defined) range types are not covered: the catalog does
not record a range's subtype.

## The fixture and the drift gate

`testutil.FixtureSQL` (`testutil/fixture.sql`) and `testutil.FixtureCatalog()`
(`testutil/catalog.go`) describe the same schema: users/posts/comments with
RLS policies on `posts`, `places`/`events` (PostGIS: a constrained
`geometry(Point,4326)` and an unconstrained `geography`), a `metrics` view,
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
make test-e2e   # DB-backed tests (fixture drift gate, pgexec RLS matrix, watch, live introspection, postgis and contrib SQL validity, metrics against a real pool) on a disposable PostGIS (project pdbcore-test, host port 5434)
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
