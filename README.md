# pdbcore

Shared engine for [pdbq](https://github.com/suprbdev/pdbq) (GraphQL) and
[pdbr](https://github.com/suprbdev/pdbr) (REST): everything about turning a
live PostgreSQL database into an API that is not specific to one transport.

> **pdbcore is the engine for pdbq and pdbr. It is v0.x and makes no external
> compatibility promise**: minor releases may change any exported API, and
> the products pin an exact version. The 1.0 promise is made by pdbq and pdbr
> for their own surfaces. See [docs/compatibility.md](docs/compatibility.md),
> [SECURITY.md](SECURITY.md), [CONTRIBUTING.md](CONTRIBUTING.md) and
> [CHANGELOG.md](CHANGELOG.md).

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
| `introspect` | `Catalog` model (`Table`, `Column`, `Constraint`, `ForeignKey`, `Index`, `Enum`, `Composite`, `Function`, `Extension`, privileges, RLS flag, comments; `Column.Spatial` (`SpatialType`: PostGIS subtype, SRID, Z/M from the geometry/geography typmod) and `Column.SQLType` (built-in modifiers such as `character varying(255)`, `numeric(10,2)`); `Catalog.ServerVersionNum` for version gating; `Column.GeneratedKind` (identity `ALWAYS` / `BY DEFAULT`, stored, virtual) with `Column.GeneratedAlways` (never writable), `Identity` and `OmittableOnInsert`; domains (`Catalog.Domains`: base type, parent domain, NOT NULL, CHECK constraints, default, comment; `Column.Domain` / `FuncArg.Domain` / `Function.ReturnDomain` refs, `Catalog.Domain`, `DomainByRef`, `DomainChain`); PostgreSQL 18 temporal keys (`Constraint.WithoutOverlaps`, `ForeignKey.Period` with `JoinPairs`, `Table.RowKeys`) and `ForeignKey.NotEnforced`; check constraints (`Table.Checks`) and the columns of index expressions and predicates (`Index.ExpressionColumns`), with `Table.ConstraintColumns` / `Table.ConstraintReferences` for integrity-error sanitizing; `ForeignKey.NotValid` and `MayDangle`; `Column.NotNull` only for validated NOT NULL constraints; `Index.OpClasses` per indexed column), `Introspect(ctx, querier, schemas)` (pg_catalog reader incl. `@enum` table conversion and installed extensions; functions ordered by schema, name and input parameter types so overload keep-first is stable; refuses PostgreSQL < 14 with `*UnsupportedServerError`), `HasExtension`, polymorphic signatures (`IsPolymorphic`, `Function.PolymorphicFamilies`, `Catalog.ResolvePolymorphic`, `Catalog.ParseTypeName` for SQL type spellings), `Clone` (deep copy for per-contract catalog hooks), `Diff` (human-readable drift lines), `Hash` (non-mutating; covers the server's major version, not its minor), `CatalogFormatVersion` (a golden test of the JSON field set fails until it is bumped) |
| `smarttags` | PostGraphile-style smart-comment parser: `Parse`, `Strip`, `Tags`, `Omits` (`@omit` and the omit tokens of `@behavior`: `-select`, `-insert`, `-update`, `-delete`, the PostGraphile V5 `-filterBy` / `-orderBy` and their deprecated forms `-filter` / `-order`), `Contracts` (`@contract` / `@behavior ±contract:<name>` membership), `Check` (unknown `@omit` actions, unknown and deprecated `@behavior` tokens against a product's `Vocabulary`), list splitting |
| `contract` | schema contracts (see [Schema contracts](#schema-contracts)): `Contract{Name, Default}` with the `Include`/`Exclude` policies (`ParsePolicy` rejects anything else), `View` (clone + filter; an unknown policy is an error) |
| `inflect` | naming pipeline types (`Kind`, `Input`, `Next`) + string helpers (`UpperCamel`, `LowerCamel`, `Singularize`, `Pluralize`, `EnumValue`, `ByColumns`); products define their own `Kind` constants and `Default` |
| `cache` | gzip+JSON catalog cache: `Save` (records the `Writer`: product, version, pdbcore version) / `Load` / `Check`, format-version (`*FormatVersionError`) and content-hash (`ErrCorrupt`) checks, 256 MiB decompression guard |
| `watch` | `New(Config)` → `Watcher`: DDL event trigger + `LISTEN/NOTIFY` (`TriggerName`, `Channel`), hash-polling fallback when the trigger cannot be installed → `OnChange(ctx, *Catalog)`; `New` validates the names, `Run` never modifies the configuration |
| `postgis` | spatial value layer: flavour detection, GeoJSON/WKT/EWKT parsing to one bound parameter, filter operators (`Ops()`), derived accessors (`Deriveds()`), transform/simplify/buffer/KNN/distance SQL (`BufferSQL`, metres on geography; `DistanceSQL`, metres on the spheroid with `SpheroidDistanceSQL`), GeoJSON output (`OutputSQL(ref, Output{JSON, Precision})`: jsonb or json, optionally with a bound coordinate precision checked by `CheckPrecision`); `postgis_raster` columns (`IsRaster`): header-only metadata `RasterAccessors()` (width, height, bands, SRID, pixel size, envelope as GeoJSON), `RasterValueSQL` (`ST_Value` at a bound point, reprojected to the raster's SRID), `RasterSummaryStatsSQL` (`ST_SummaryStats` of a band as a JSON object), `RasterIntersectsSQL` (served by a GiST index on `ST_ConvexHull(col)`) and `Raster*Cost` weights for cost estimators |
| `contrib` | operator vocabularies + value coercion for full-text search (`tsvector`, and `to_tsvector` over text and json/jsonb documents, with `ts_headline` snippets), `pg_trgm`, `unaccent`, `fuzzystrmatch`, `hstore`, `ltree`, `citext`, `intarray`, range/multirange, `inet`/`cidr`, pgvector (`vector`, `halfvec`, `sparsevec`, plus bit distances on `bit`/`varbit`), `cube` and `earthdistance` (`earth` columns, `<@>` on `point`) columns (see [Contrib vocabularies](#contrib-vocabularies)): `ForColumn`/`ForType` lookup bound to the extension's schema, filter `Op`s, ordering `Expr`s (`Expr.Score` marks the selectable similarity/distance values, `Expr.Derived` the per-use parameterised values such as an hstore key's value, `Expr.OrderSQL` the index-served ordering form), `Accessor`s, value `Codec`s, `Op.RankSQL`/`Op.HeadlineSQL` with validated `HeadlineOptions`, `HeadlineDocumentSQL`/`HeadlineDocumentCost` to bound and price a headline's document |
| `auth` | `Authenticator`: JWT (HS*, JWKS RS*/ES* with a kid-matched cache refreshed by one shared background fetch, stale keys served for one extra TTL during endpoint outages, failure backoff, endpoint details logged and never returned to clients), trusted-gateway claim headers with `TrustedProxies` (one spelling per claim: `User-Id` is `user_id`; underscores, repeats and case-folded duplicates are rejected; optional `HeaderClaims` allowlist; the gateway must strip every client-supplied prefixed header), typed `Mode` (`jwt`, `headers`, `none`; `New` rejects anything else, a headers mode without trusted proxies, and a jwt mode without a key), case-insensitive `Bearer`, clock-skew `Leeway`, role claim paths (`app_metadata.role`, `realm_access.roles[0]`), role resolution and allowlist, optional anonymous access, `SkipRoleSwitch`; `ErrInvalidToken` / `ErrAuthRequired` / `ErrRoleNotAllowed`, and `Challenge` for the `WWW-Authenticate` value |
| `pgexec` | pool `Connect` (connections to PostgreSQL < 14 fail), `RunTx` (isolation from `ParseIsolation`, which rejects unknown spellings, `SET LOCAL ROLE`, claims via `set_config(prefix.k, v, true)` and optionally as one JSON setting, 40001/40P01 retries), `QueryTx` (one pipelined round trip), `Savepoint` (run a step in a SAVEPOINT inside a `RunTx` callback; a failure rolls back to it and comes back as `*RolledBackError`, so the transaction goes on), `Observer` (per-attempt duration and error via `TxOptions.Observer`, `Observe` for direct statements), `Identity`, `Classify` PG errors into classes and client messages under a `Detail` policy (`ParseDetail`: `dev`/`prod`/`strict`, anything else rejected; the one error-message API) (21* cardinality violations, malformed tsquery/ltree operands, unknown text search configs and raster/geometry SRID mismatches are user input), `QuoteIdent`/`ValidRole`/`ValidClaimKey` |
| `jwtmint` | composite-typed function results → signed HS256 JWT (`MintsFunction`, `Mint`) |
| `httpx` | CORS (`CORSPolicy`: origins, preflight methods, exposed headers) + preflight, `AddVary`, gzip, `Healthz`, `ListenAndServe` with the standard timeouts and graceful shutdown |
| `metrics` | Prometheus instrumentation on a per-instance registry (or a caller's `Registerer` + `ConstLabels`): HTTP `Middleware` (requests, duration, in flight by route class, method, status class; `SetRoute`), `DBObserver` (a `pgexec.Observer`: duration, errors by SQLSTATE class, retries), `RegisterPool` (pgxpool `Stat`), `SchemaRebuilt`, `ObserveCompile`, product counters via `NewCounter`; `NewRegistry` + `RegisterPoolOn` for several instances (one per contract) on one registry; a nil `*Metrics` records nothing (see [Metrics](#metrics)) |
| `confx` | strict koanf `Load` (defaults < YAML < env < `WithFlags` command-line values, which `confx/cliflags.Changed` reads from a pflag set; unknown keys are errors naming the key they moved to (`WithMoved`), why they were removed (`WithRemoved`) or the closest valid one), env key mapper (`_` → `.`, `__` → `_`, comma-separated lists), `ExampleYAML` from `koanf`/`doc` tags, `Discover`, shared config sections (`Database`, `Schema`, `Filters`, `Auth`/`JWT`/`Headers`, `Roles`, `Claims`, `TX`, `Watch`, `Errors`, `Plugins[S]` with typed per-plugin settings, `Log`, `Metrics`, `Contract` list entries) + `Validate*` helpers (`ValidateIdentity`, `ValidatePluginNames`, `ValidateContracts`), `AuthConfig(Auth, Roles)`, `IdentityWarnings` |
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
	pgexec.Identity{Role: role, Claims: claims, ClaimsPrefix: "jwt.claims"},
	func(q pgexec.Querier) error { return q.QueryRow(ctx, sql, args...).Scan(&data) })
```

`Introspect` leaves out relations an extension owns (`pg_depend` deptype
`e`), as it does extension functions: PostGIS's `spatial_ref_sys`,
`geometry_columns`, `geography_columns`, `raster_columns` and
`raster_overviews`, `pg_buffercache`'s view and so on. Catalog views such as
`geometry_columns` list the columns of every table, including ones the API
hides. To expose one anyway, create a view of your own over it in an
exposed schema; the view is yours, not the extension's, and it is
introspected like any other.

An integrity error (class 23) names its constraint or index, and those
names usually spell out their columns. Before passing such a message to a
client, call `t.ConstraintReferences(pgErr.ConstraintName, hidden)` on the
error's table: it reports whether the primary key, unique, foreign key or
check constraint, or the index, of that name references a column `hidden`
rejects, counting the columns of key expressions (`lower(secret)`),
partial-index predicates (`WHERE secret LIKE 'vip%'`), exclusion
constraints and check constraints, and every column for a whole-row
reference. Its second result is false when the table records nothing by
that name (a partition's own index, for example); treat that as hidden
whenever the table has a hidden column.

Both products embed the `confx` sections in their own `Config` struct (the
product owns `Server` and its defaults such as `watch.channel` and the claim
header prefix), wrap `plugin.Registry` with their transport-specific hooks,
and register `smartcomments/catalog.New()` inside their smart-comments
plugin. `claims.prefix` defaults to `jwt.claims` (PostGraphile's namespace)
in both, and `claims.json_setting` can add PostgREST's `request.jwt.claims`,
so RLS policies work unchanged behind either server. The claim header
prefix stays per product (`X-Pdbq-Claim-` / `X-Pdbr-Claim-`): names SQL
sees are product-neutral so policies port, while names on the wire between
a gateway and the server are namespaced like every other product header; `watch.TriggerName` differs
(`pdbq_watch` / `pdbr_watch`) so both can watch one database.

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
cfg.OnChange = func(ctx context.Context, cat *introspect.Catalog) { m.SchemaRebuilt(rebuild(ctx, cat)) } // watch.Config
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
| `jsonsearch` | `json`, `jsonb` | same four, over `to_tsvector([config,] col)`, which searches the document's string values (not keys, numbers or booleans) | — | — | as `textsearch` |
| `trgm` (`pg_trgm`) | text-like | `similarTo` (`%`), `wordSimilarTo` (`<%`), `strictWordSimilarTo` (`<<%`) | `similarity`, `wordSimilarity`, `strictWordSimilarity` (scores); `distance` (`<->`), `wordDistance` (`<<->`), `strictWordDistance` (`<<<->`) for ordering | — | GIN/GiST `gin_trgm_ops`/`gist_trgm_ops`; KNN ordering GiST only; none on `bpchar` (only an expression index on `col::text`). Word ops need pg_trgm 1.2, strict ones 1.4 (`Vocabulary.ForVersion`, applied by `ForColumn` from `Extension.Version`) |
| `unaccent` | text-like | `unaccentEqualTo` (`lower(unaccent(col)) = lower(unaccent($1))`), `unaccentLike` (`unaccent(col) ILIKE unaccent($1)`), `unaccentIncludes`, `unaccentStartsWith` (operand wildcards escaped in the parameter); all accent- and case-insensitive. Rendered as `"ext".unaccent('"ext"."unaccent"'::regdictionary, …)`: the one-argument form finds its dictionary through the `search_path` | — | — | none (`unaccent` is not immutable) |
| `fuzzy` (`fuzzystrmatch`) | text-like | `levenshteinDistanceLessThan` (`levenshtein_less_equal(col, $1, $2) < $2`; operand `{text, distance}`, `ArgTextDistance`), `soundexEqualTo`, `metaphoneEqualTo` (`metaphone(…, 255)`), `doubleMetaphoneEqualTo` (`dmetaphone`, primary code) | `levenshteinDistance` (`int4`), nearest-first ordering and score. Reference texts over 255 characters (metaphone: bytes) are refused; longer column values yield NULL instead of fuzzystrmatch's error | — | none (only expression indexes, which do not match) |
| `vector` (pgvector) | `vector`, `halfvec`, `sparsevec` | `<distance>LessThan` per metric (`((col) <=> $1::vector) < $2::float8`; operand `{vector, distance}`, `ArgVectorDistance`; no index serves it) | `l2Distance` (`<->`), `negativeInnerProduct` (`<#>`), `cosineDistance` (`<=>`), `l1Distance` (`<+>`, pgvector 0.7) for nearest-first ordering and as scores, operand cast to the column type; `dims` (`vector_dims`; not on `sparsevec`), `norm` (`vector_norm` / `l2_norm`) | list of numbers or the text form ↔ `$1::"ext".vector`; `vector`/`halfvec` out as a JSON array (`(col)::text::jsonb`), `sparsevec` as its text (`{1:1.5}/3`; a dense list input is converted) | HNSW `<type>_<metric>_ops` (`vector_l2_ops`, …), IVFFlat except L1 and `sparsevec` (`IndexHint.Ordering`) |
| `bit` (pgvector 0.7+) | `bit`, `varbit` (core types; only with pgvector installed) | `hammingDistanceLessThan`, `jaccardDistanceLessThan` | `hammingDistance` (`<~>`), `jaccardDistance` (`<%>`), ordering and scores; operand a bit string or a list of 0/1/booleans, `$1::varbit` | — | HNSW `bit_hamming_ops` / `bit_jaccard_ops`, IVFFlat `bit_hamming_ops`; a `bit(n)` column holding `binary_quantize(embedding)` re-ranked by the full vector |
| `citext` | `citext` | — (text vocabularies apply) | — | `$1::"ext".citext`; use `Vocabulary.Operator("=")` — unqualified or text-typed comparisons fall back to case-sensitive `text` operators | — |
| `hstore` | `hstore` | `hasKey` (`?`), `hasAllKeys` (`?&`), `hasAnyKeys` (`?\|`), `contains` (`@>`), `containedBy` (`<@`), `hasDefinedKey` (`defined`) | `value` (`col -> $1::text`, derived, key bound) | JSON object of string → string\|null ↔ `hstore($1::text[])` / `hstore_to_jsonb` | GIN `gin_hstore_ops`, GiST `gist_hstore_ops` (not `<@`) |
| `ltree` | `ltree` | `ancestorOf` (`@>`), `descendantOf` (`<@`), `ancestorOfAny` / `descendantOfAny` (`ltree[]` operand), `matchesLquery` (`~`), `matchesAnyLquery` (`?` `lquery[]`), `matchesLtxtquery` (`@`) | `nlevel`, `parent` (`subpath(col, 0, nlevel(col) - 1)`, NULL for the empty path); derived `lca` (with a path) and `subpath` (`ArgSubpath` `{offset, length?}`, NULL when offset lies outside the path instead of ltree's error) | text ↔ `$1::"ext".ltree` | GiST `gist_ltree_ops` (not the `*Any` forms) |
| `ltree[]` | `ltree[]` | same names, "some element …" | `lca` (of the non-null elements) | list ↔ `$1::"ext".ltree[]` | GiST `gist__ltree_ops` (not `ancestorOf`) |
| `range` | `int4range`, `int8range`, `numrange`, `tsrange`, `tstzrange`, `daterange` | `overlaps` (`&&`), `contains` (`@>`), `containsElement` (`@>` element), `containedBy` (`<@`), `strictlyLeftOf` (`<<`), `strictlyRightOf` (`>>`), `notExtendsRightOf` (`&<`), `notExtendsLeftOf` (`&>`), `adjacentTo` (`-\|-`) | `start`, `end`, `startInclusive`, `endInclusive`, `startInfinite`, `endInfinite`, `isEmpty` | `{start, end, startInclusive, endInclusive, isEmpty}` or a literal (`"[1,5)"`) ↔ `int4range($1::int4, $2::int4, $3::text)` / jsonb object | GiST, SP-GiST |
| `multirange` | the six built-in multiranges (PG 14+) | same, with a range operand | same | list of ranges or a literal (`"{[1,3), [5,7)}"`) ↔ `int4multirange(VARIADIC ARRAY(… unnest($1, $2, $3) …))` / jsonb array | GiST |
| `intarray` | `int4[]` (with `intarray` installed) | `matchesQueryInt` (`@@ $1::"ext".query_int`, `1&(2\|3)`); arrays with NULL elements raise 22004 | `icount` | — | GIN `gin__int_ops`, GiST `gist__int_ops` / `gist__intbig_ops` |
| `cube` | `cube` | `contains` (`@>`), `containedBy` (`<@`), `overlaps` (`&&`), `<distance>LessThan` per metric (`col && cube_enlarge($1::cube, $2, cube_dim($1::cube)) AND (col <-> $1::cube) < $2`; operand `{cube, distance}`, `ArgCubeDistance`) | `l2Distance` (`<->`), `l1Distance` (`<#>`), `chebyshevDistance` (`<=>`), ordering and scores | a list of numbers (point), a pair of corner lists (box) or the text form ↔ `$1::"ext".cube`; out as the same JSON shape | GiST: operators, KNN ordering and the thresholds' overlap prefilter |
| `earth` (`earthdistance`) | columns declared as the `earth` domain (or a domain over it; `ForColumn` only, `ForType` sees the base `cube`) | `earthDistanceLessThan` (`earth_box(ll_to_earth($1, $2), $3) @> col AND earth_distance(...) < $3`; operand `{lat, lon, distance}` in metres, `ArgLatLonDistance`) | `earthDistance` (metres, `earth_distance`) as a score, ordered by `Expr.OrderSQL` = cube's `col <-> ll_to_earth($1, $2)` (the chord, same order, KNN); `latitude`, `longitude` | `{lat, lon}` ↔ `ll_to_earth($1::float8, $2::float8)` / `{lat, lon}` object | GiST on the column. earthdistance before 1.2 (PostgreSQL ≤ 15) needs its and cube's schemas on the `search_path` |
| `earthpoint` (`earthdistance`) | `point` (core; only with earthdistance installed), (lon, lat) | `earthDistanceMilesLessThan` (`(col <@> point($2, $1)) < $3`, statute miles) | `earthDistanceMiles` (`<@>`), ordering and score | — | none |
| `inet` | `inet`, `cidr` | `containedBy` (`<<`), `containedByOrEquals` (`<<=`), `contains` (`>>`), `containsOrEquals` (`>>=`), `containsOrContainedBy` (`&&`) | `family`, `masklen`, `host`, `network` | text ↔ `$1::inet` / `$1::cidr` | GiST `inet_ops`, SP-GiST |

Output SQL comes in two flavours. `Codec.OutputSQL`, `RangeOutputSQL`, `MultirangeOutputSQL` and `HstoreOutputSQL` yield `jsonb`; their `OutputJSONSQL` counterparts yield `json` (in `postgis`, `OutputSQL`, `Derived.SQL` and `RasterAccessor.SQL` take an `Output` with `JSON: true` instead), which skips jsonb construction and is markedly cheaper. Use json when the value is only embedded in a `json_build_object`/`json_agg` response; keep jsonb when it is compared, grouped, deduplicated or aggregated with jsonb functions.

Full-text matches also render a highlighted snippet: `Op.HeadlineSQL(doc,
options, phs...)` is `ts_headline([config,] doc, <the op's tsquery>[,
options::text])` over the same placeholders as the predicate. `doc` is the
filtered column for `textsearch`/`jsonsearch`, or another document column
(`IsDocument`: text-like, json, jsonb) holding the source of a `tsvector`.
`ParseHeadlineOptions` validates the options (`maxWords`, `minWords`,
`shortWord` ≤ `MaxHeadlineWords`, `maxFragments` ≤ `MaxHeadlineFragments`,
`highlightAll`, and `startSel`/`stopSel`/`fragmentDelimiter` of at most
`MaxHeadlineMarkup` bytes without `"`, `\` or control characters) and
`HeadlineOptions.String()` renders the options text, which is bound as one
parameter. ts_headline parses the whole document on every call, so its cost
grows with the document: about a millisecond per 8192 characters
(`HeadlineCostChars`) on PostgreSQL 18, over 100 ms for a 1 MB text. Bound
the document with `HeadlineDocumentSQL(doc, pgType, maxLenPh)` and pass the
result as `doc`: a text-like document becomes `left(doc, $n::int4)` (the
headline comes from its first n characters), and a json/jsonb document
longer than n characters as text yields a NULL headline. Bind n as a
parameter (`MaxHeadlineDocument`, 65536, is a default; make it
configurable) and price each headline at `HeadlineDocumentCost(n)` column
reads per returned row: `HeadlineCost` per started `HeadlineCostChars`
characters, 80 at the default. Consumers evaluate headlines only on
returned rows (`postgis.BufferCost` prices a buffer projection). The flat
`HeadlineCost` is only honest for documents of up to `HeadlineCostChars`
characters; with unbounded documents a few aliased headlines can keep a
backend busy until `statement_timeout`.

Each `Op` also carries `Arg` (operand kind), `Description`, `Index`
(`IndexHint{Method, OpClass, Note, Ordering}`, matched against an index
column with `IndexHint.Serves(method, opClass)`) and `Extension`; accessor and
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

Semver `v0.x.y`, and it stays there (see
[docs/compatibility.md](docs/compatibility.md)): minor bumps may break API,
patch bumps may not; products pin an exact version. `introspect.CatalogFormatVersion` is
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
service. pgvector is not part of the postgis/postgis images: CI installs
`postgresql-$PG_MAJOR-pgvector` into the service container from the image's
PGDG apt source (best effort), and the pgvector tests skip when
`CREATE EXTENSION vector` is unavailable; `make test-e2e` installs it the
same way.

## Release flow

pdbcore is a library, so a release is a tag: `make release` verifies the tree
is clean, prompts for `vX.Y.Z`, runs `make test lint`, tags and pushes; the
`Release` workflow (`.github/workflows/release.yaml`) re-runs the race tests
and creates the GitHub release with generated notes (no binaries; an SBOM is attached;
publishing is skipped under `act`).

Consumers then bump with `go get github.com/suprbdev/pdbcore@vX.Y.Z`. Release
order is pdbcore, then pdbq, then pdbr. The workflow also publishes a
CycloneDX SBOM with a build provenance attestation. See
[CONTRIBUTING.md](CONTRIBUTING.md#releasing).
