# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
as described in [docs/compatibility.md](docs/compatibility.md).

Entries up to and including the latest 0.x release below were seeded from the
GitHub release notes; the Unreleased section is maintained by hand and
becomes the release notes of the next tag.

## [Unreleased]

### Added

- SECURITY.md, CONTRIBUTING.md, CHANGELOG.md and a versioning, deprecation and
  supported-versions policy ([docs/compatibility.md](docs/compatibility.md)).
- Release artifacts are accompanied by a CycloneDX SBOM, a keyless cosign
  signature of the checksums and GitHub build provenance attestations.
- Compatibility policy: pdbcore stays v0.x and is the engine for pdbq and
  pdbr, with no external compatibility promise.
- A CI job builds and tests pdbq and pdbr at their `main` against the pull
  request.
- `smarttags`: `Omits` maps the PostGraphile V5 `@behavior` spellings
  `-filterBy` and `-orderBy` onto the `filter` and `order` actions; the old
  `-filter` and `-order` stay as deprecated aliases.
- `smarttags.Tags.Check` reports unknown `@omit` actions, unknown `@behavior`
  tokens and deprecated `@behavior` tokens against a product's
  `smarttags.Vocabulary`; `smartcomments/catalog.Check` runs it over every
  commented object of a catalog.

### Fixed

- `introspect.Introspect` no longer sorts the caller's schema slice in
  place (a live boot alphabetised the products' `schema.schemas`, which a
  cache boot did not); `Catalog.Schemas` keeps the configured order and
  `Hash` ignores it.
- Strict environment loading reports a variable that doubles the
  underscore after its section (`PDBQ_ROLES__ALLOWED`) with a did-you-mean
  hint instead of ignoring it.
- `confx.ValidateDatabase` rejects `database.max_conns` below 1, a
  non-positive `database.connect_timeout` and a negative
  `database.statement_timeout`; `ValidateIdentity` rejects a non-positive
  `auth.jwt.jwks_cache_ttl` with a JWKS URL (it silently became one hour);
  `ValidateWatch` and `watch.New` reject a non-positive poll interval (the
  polling fallback panicked).

### Breaking: API hygiene

See [docs/upgrading.md](docs/upgrading.md#api-hygiene).

- `confx.Load` takes `WithFlags(values)` instead of a pflag set;
  `confx/cliflags.Changed` replaces `confx.ChangedFlags`. `WithRemoved`
  names keys removed without a replacement. `MovedKeys`,
  `ParseTrustedProxy`, `ValidChannelName` and `TX.Mutations` are gone, and
  the shared doc tags no longer name a product.
- `pgexec.SanitizeMessage` is gone (use `Classify`); `ParseIsolation` and
  `ParseDetail` return an error instead of falling back.
- `contract.ParsePolicy`; `confx.Contract.Default` is a `contract.Policy`;
  `Contract.View` returns an error for an unknown policy; `Apply`,
  `OmitAll` and `TransformCatalog` are gone.
- `postgis.OutputSQL(ref, Output)`, `Derived.SQL(ref, f, Output)` and
  `RasterAccessor.SQL(ref, Output)` replace the json/jsonb × precision
  variants; `Ops()`, `Deriveds()`, `RasterAccessors()` and
  `contrib.VectorTypes()` replace the mutable variables; `Op.Index` and
  `Op.Arg` are `Op.Indexable` and `Op.OperandType`.
- `jwtmint.Sign`, `postgis.OpsDoc`, `OpNames` and `DerivedSuffixes` are
  gone.
- `watch.New(Config)` validates up front; `OnChange(ctx, cat)`; `Run` no
  longer writes into the watcher's configuration.

## [0.18.0] - 2026-11-23

### Breaking: identity and strict config

- `confx`: `rls` is replaced by `auth` / `roles` / `claims`; `ValidateIdentity`, `AuthConfig(Auth, Roles)`; `DefaultClaimsPrefix` = `jwt.claims`; optional `claims.json_setting`.
- `auth`: `New` returns `(*Authenticator, error)` and rejects unknown modes, jwt without a key source and headers mode without trusted proxies; typed `auth.Mode`; empty `AnonymousRole` → `ErrAuthRequired` (`auth.Challenge` for `WWW-Authenticate`); `SkipRoleSwitch` replaces `Enabled`; leeway and role claim paths.
- Strict `confx.Load`: unknown keys (file and prefixed env) are errors with moved-to / did-you-mean hints; generic typed `Plugins[S]` settings.
- `httpx`: `CORSPolicy{Origins, AllowMethods, ExposeHeaders}`; CORS appends to `Vary`.
- `pgexec`: SQLSTATE class 21 is user input; per-claim settings only with a non-empty prefix.

### Changes
* feat(auth)!: split auth, roles and claims config; strict config (#83)

## [0.17.0] - 2026-11-23

### Breaking: catalog format 10

- `GeneratedIdentity` is split into `GeneratedIdentityAlways` / `GeneratedIdentityByDefault`; `Column.Generated` is replaced by `GeneratedAlways()`, `OmittableOnInsert()` and `Identity()`.
- `Catalog.Hash()` no longer mutates the catalog and ignores the PostgreSQL minor version.
- Functions are ordered by schema, name and argument types, so overload keep-first is stable.
- `introspect/testdata/catalog_format.golden` fails the build if the JSON shape changes without a `CatalogFormatVersion` bump.
- Cache envelopes record their writer (`cache.Writer`); typed errors `*cache.FormatVersionError` and `cache.ErrCorrupt`.
- PostgreSQL < 14 is refused (`*introspect.UnsupportedServerError`, and in `pgexec.Connect`).

### Changes
* feat(introspect)!: catalog format 10 (identity kind, deterministic overloads, pure Hash) (#82)

## [0.16.0] - 2026-11-23

### Highlights

- **Extension-owned relations are skipped** (PostGIS `geometry_columns`, `geography_columns`, `raster_columns`, `raster_overviews`, `spatial_ref_sys`, …), like extension-owned functions.
- **Constraint columns**: `Index.ExpressionColumns` (columns used by key expressions and partial-index predicates), `Table.Checks`, and `Table.ConstraintReferences` / `ConstraintColumns` for integrity-error sanitizers.
- **Index operator classes**: `Index.OpClasses` / `OpClass(i)`, `contrib.IndexHint.Serves(method, opClass)`.
- **`NOT NULL … NOT VALID`** (PostgreSQL 18+): `Column.NotNull` requires a validated constraint; `ForeignKey.NotValid` / `MayDangle()`.
- **Headline bound**: `contrib.HeadlineDocumentSQL`, `MaxHeadlineDocument`, `HeadlineDocumentCost`.
- `CatalogFormatVersion` 9.

### Changes
* fix(introspect): skip extension-owned relations; record expression and predicate columns (#75)
* feat(introspect): NOT VALID constraints and index operator classes (#74)

## [0.15.0] - 2026-11-23

### Highlights

- **cube**: `contains` / `containedBy` / `overlaps`, `l2` / `l1` / `chebyshev` distances (ordering, scores) and GiST-served `<metric>DistanceLessThan` thresholds; JSON point/box codec.
- **earthdistance**: earth columns as `{lat, lon}`, `earthDistance` score in metres, `Expr.OrderSQL` (cube `<->` chord distance for KNN ordering and keyset cursors), `earthDistanceLessThan` with an `earth_box` prefilter; point `<@>` miles surface.
- **Raster**: `IsRaster`, header accessors (width, height, bands, SRID, pixel size, envelope), `RasterValueSQL` (`ST_Value` at a bound point and band), `RasterSummaryStatsSQL`, `RasterIntersectsSQL`; cost weights. "Raster and geometry do not have the same SRID" is classified as user input.

### Changes
* feat(contrib): cube and earthdistance; feat(postgis): raster columns (#70)

## [0.14.0] - 2026-11-23

### Highlights

- **GeoJSON precision**: `OutputPrecisionSQL` / `OutputJSONPrecisionSQL` render `ST_AsGeoJSON(ref, $n::int4)` with the digits bound (`CheckPrecision`: 0–15).
- **Buffer**: `BufferSQL` (`ST_Buffer(ref, $n::float8)`; metres on geography), `CheckBuffer`.
- **jsonb full-text search**: `jsonsearch` vocabulary gives json/jsonb columns the `matches*` operators and rank over `to_tsvector([config,] col)`.
- **Headlines**: `Op.HeadlineSQL` renders `ts_headline` with the match's own tsquery; `ParseHeadlineOptions` validates and bounds options, rendered as one bound parameter. Shared cost weights `HeadlineCost` (10) and `BufferCost` (5).

### Changes
* feat(postgis): GeoJSON precision; feat(contrib): jsonb full-text search and headlines (#69)

## [0.13.1] - 2026-11-23

### Security

- **Polymorphic resolution and hidden types.** `@anyelement` / `@anycompatible` tags could resolve to the row type of a contract-hidden or fully omitted table, keeping such functions inside a contract. Hidden relations are now recorded on the catalog (`Catalog.HideRelation` / `RelationHidden`, kept by `Clone`) and resolution refuses them; it also only resolves to built-in types or types the introspected catalog knows (non-introspected schemas are refused). No consumer changes needed.

### Changes
* fix(contract): polymorphic resolution never resolves to a hidden table type (#68)

## [0.13.0] - 2026-11-23

### Highlights

- **hstore**: `value` expression (`col -> key`, key bound) and `hasDefinedKey`.
- **ltree**: `parent`, `lca` (with a path; also on `ltree[]`) and `subpath` (`{offset, length?}`) accessors alongside `nlevel`.
- **fuzzystrmatch**: Levenshtein distance score, ordering and `levenshteinDistanceLessThan` threshold (computed with `levenshtein_less_equal`); `soundexEqualTo`, `metaphoneEqualTo`, `doubleMetaphoneEqualTo`.
- **intarray**: `matchesQueryInt` (`@@ query_int`, GIN/GiST index hints) and `icount`.
- New `Expr.Derived` flag, operand kinds `ArgTextDistance` / `ArgSubpath`, and an `Integer` coercion helper.

### Changes
* feat(contrib): hstore and ltree accessors, fuzzystrmatch, intarray (#66)

## [0.12.0] - 2026-11-23

### Changes
* feat(contrib): score expressions, pgvector threshold filters and bit distances (#65)

## [0.11.0] - 2026-11-23

### Highlights

- **pgvector** (`contrib.Vector`): `vector`, `halfvec`, `sparsevec`; distance expressions (`l2Distance`, `negativeInnerProduct`, `cosineDistance`, `l1Distance` on pgvector 0.7+), HNSW/IVFFlat index hints, `dims` / `norm` accessors, JSON-array codec.
- **unaccent** (`contrib.Unaccent`): `unaccentEqualTo`, `unaccentLike`, `unaccentIncludes`, `unaccentStartsWith` (two-argument `unaccent(regdictionary, …)`, safe when the extension schema is off the search_path).
- **PostGIS**: `bboxIntersects2D` (`&&` against a geometry); on geography columns `Perimeter` is now in metres and `Centroid` is spheroidal.

### Changes
* feat: pgvector and unaccent vocabularies, bboxIntersects2D, geography measures (#62)

## [0.10.0] - 2026-11-23

### Highlights

- **Polymorphic signatures**: `introspect` helpers parse SQL type spellings and rewrite polymorphic functions to concrete types; the smart-comments catalog hook applies `@anyelement <type>` / `@anycompatible <type>`. Fixture gains row-argument, row-array and polymorphic functions.
- **Watcher**: after installing its event trigger, `Run` waits (bounded by `Watcher.InstallWait`, default 30s) for transactions open at install time to end before the startup re-check, so DDL committed by them is no longer missed; `Ready` closes after that re-check.

### Changes
* fix(watch): wait for transactions open at trigger install before the startup re-check (#64)
* feat(functions): resolve polymorphic signatures; fixture functions with row and polymorphic args (#61)

## [0.9.0] - 2026-11-23

### Highlights

- **`pgexec.Savepoint`**: run a function inside a SAVEPOINT in a `RunTx` callback; a failure rolls back to the savepoint and returns `*RolledBackError` so the transaction continues. Serialization failures, deadlocks, `ErrRollback` and cancelled contexts still abort the transaction (and retry).

### Changes
* feat(pgexec): Savepoint helper for partial transaction recovery (#46)

## [0.8.1] - 2026-11-23

### Changes
* fix(pgexec): don't count a requested rollback as a database error (#44)

## [0.8.0] - 2026-11-23

### Highlights

- **Schema contracts**: named views of one catalog (e.g. `public`, `partner`, `internal`) that pdbq and pdbr each serve as a separate API. Membership comes from smart comments: `@contract a, b` and `@behavior +contract:x` / `-contract:x`, with a per-contract default policy (`include` / `exclude`). A contract only removes surface: hidden tables (with their partitions, foreign keys and row-typed functions), functions and foreign keys are dropped; hidden columns get a bare `@omit` so each product hides them everywhere.
- `introspect.Catalog.Clone()` (deep copy), `confx.Contract` config with validation, and per-contract metrics on a shared registry (`metrics.NewRegistry`, `RegisterPoolOn`).

### Changes
* feat(contracts): schema contract tags and catalog views (#42)

## [0.7.1] - 2026-11-23

### Changes
* fix(watch): catch DDL committed during startup; add Ready signal (#41)

## [0.7.0] - 2026-11-23

### Security

- **Claim spoofing in headers auth mode (fixed).** Claim headers such as `X-…-Claim-User-Id` and `X-…-Claim-User_id` (or case/separator variants) used to map to the same claim, and which value won was random per request, so a client behind a gateway could override a claim the gateway set. Each claim now has exactly one header spelling (`X-<prefix>-Claim-<Words-Joined-By-Hyphens>`); malformed, repeated or colliding claim headers are rejected with 401. New optional allowlist `rls.auth.header_claims`. Gateways must strip every client-supplied header with the claim prefix, in any letter case.
- **JWKS hardening.** Key fetches no longer serialize requests behind a global lock (forged tokens could stall every authenticated request for 10 s at a time); fetches are shared, run in the background with backoff on failure, stale keys keep verifying for one extra TTL during an IdP outage, and client errors no longer reveal the JWKS URL (it is logged server-side via `auth.Config.Logger`).

### Breaking changes

- Headers-mode requests with underscore, malformed, repeated or conflicting claim headers now get 401.
- `rls.auth.header_prefix`, `role_claim` and `header_claims` are validated as header-safe names in headers mode.

### Changes
* ci: run PG 14 and 19 on PRs, full matrix and docker on main (#34)
* ci: install postgresql-client only when psql is missing (#35)
* fix(auth): reject colliding claim headers and harden JWKS fetching (#39)

## [0.6.0] - 2026-11-23

### Changes
* feat(metrics): add Prometheus instrumentation, pgexec observer and confx metrics section (#32)
* feat(introspect): record domains as named types (#33)

## [0.5.0] - 2026-11-23

### Changes
* introspect: index catalog lookups, skip type-owned functions (#27)
* Add json output variants for postgis, hstore and range SQL (#29)

## [0.4.0] - 2026-11-23

### Security

**RLS bypass through role "none" (fixed).** Earlier versions accepted the role name `none` from a role claim (JWT or headers mode) when `rls.allowed_roles` was empty or listed it. PostgreSQL treats `SET ROLE none` as `RESET ROLE`, so such a request ran as the database connection's login role — often the table owner or a superuser — and row-level security did not apply. Role names `none`, `public` and `pg_*` are now rejected at config load (`rls.anonymous_role`, `rls.default_role`, `rls.allowed_roles`) and per request. Deployments should set `rls.allowed_roles` explicitly and should not connect as a superuser or table owner. The claims prefix is validated too (real GUC namespaces such as `plpgsql` or `pg_*` are refused).

### Highlights

- `pgexec.QueryTx`: RLS identity, claims and statements in one round trip (anonymous ~3x, 7 claims ~5x ops/s vs `RunTx` in benchmarks); `RunTx` claims now applied in one statement.
- watch: event triggers filter by watched schema (temp/other-schema DDL ignored), `OnChange` skipped when the catalog hash is unchanged, `Watcher.InitialHash`, sql_drop coverage, pinned `search_path`.
- PostGIS operand SQL is one value-independent expression per column type (stable prepared statements).
- `Column.BaseTypeSchema`; extension types matched by schema; custom range types (`Catalog.Ranges`) get contrib range vocabularies.
- `CatalogFormatVersion` 7 — regenerate schema caches.

### Changes
* Value-independent PostGIS operand SQL; base type schemas and range subtypes in the catalog (#25)
* pgexec: one-round-trip QueryTx for RLS; watch: filter DDL and skip unchanged catalogs (#26)

## [0.3.0] - 2026-11-22

### Changes
* Fix Singularize for words ending in sys/us/is and uncountables (#15)
* postgis: ParseFor column SRID, classify PostGIS input errors (#16)
* introspect: PostgreSQL 17-19 catalog support (temporal keys, NOT ENFORCED, virtual columns) (#17)
* Introspect function defaults/VARIADIC/OUT columns; skip partition clones (#21)
* contrib: operator vocabularies for full-text, trgm, hstore, ltree, citext, ranges, inet (#18)

## [0.2.0] - 2026-11-22

### Changes
* ci: test e2e against PostgreSQL 14-19 (#8)
* Introspect extensions and PostGIS column typmods (#9)
* postgis: connection-filter-postgis operator parity and more accessors (#10)

### New Contributors
* @archlemon made their first contribution in https://github.com/suprbdev/pdbcore/pull/8

## [0.1.0] - 2026-09-23

Initial release.

[Unreleased]: https://github.com/suprbdev/pdbcore/compare/v0.18.0...HEAD
[0.18.0]: https://github.com/suprbdev/pdbcore/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/suprbdev/pdbcore/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/suprbdev/pdbcore/compare/v0.15.0...v0.16.0
[0.15.0]: https://github.com/suprbdev/pdbcore/compare/v0.14.0...v0.15.0
[0.14.0]: https://github.com/suprbdev/pdbcore/compare/v0.13.1...v0.14.0
[0.13.1]: https://github.com/suprbdev/pdbcore/compare/v0.13.0...v0.13.1
[0.13.0]: https://github.com/suprbdev/pdbcore/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/suprbdev/pdbcore/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/suprbdev/pdbcore/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/suprbdev/pdbcore/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/suprbdev/pdbcore/compare/v0.8.1...v0.9.0
[0.8.1]: https://github.com/suprbdev/pdbcore/compare/v0.8.0...v0.8.1
[0.8.0]: https://github.com/suprbdev/pdbcore/compare/v0.7.1...v0.8.0
[0.7.1]: https://github.com/suprbdev/pdbcore/compare/v0.7.0...v0.7.1
[0.7.0]: https://github.com/suprbdev/pdbcore/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/suprbdev/pdbcore/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/suprbdev/pdbcore/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/suprbdev/pdbcore/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/suprbdev/pdbcore/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/suprbdev/pdbcore/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/suprbdev/pdbcore/releases/tag/v0.1.0
