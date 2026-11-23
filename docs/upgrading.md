# Upgrading

## 0.x → 1.0

### Catalog format 10

`introspect.CatalogFormatVersion` is 10. Schema caches written by earlier
releases (format 9) are refused: regenerate them with `pdbq schema dump` or
`pdbr schema dump`.

- **`Column.Generated` is gone.** `Column.GeneratedKind` alone says whether
  and how a column is generated, and identity columns now record their kind:
  `GeneratedIdentity` (`"identity"`) is replaced by `GeneratedIdentityAlways`
  (`"identity_always"`) and `GeneratedIdentityByDefault`
  (`"identity_by_default"`). Replace `c.Generated` with the question being
  asked:
  - `c.GeneratedAlways()`: PostgreSQL computes every value, so writes must
    not set the column (identity `ALWAYS`, stored and virtual generated
    columns). Identity `BY DEFAULT` columns are writable, like serial ones.
  - `c.OmittableOnInsert()`: an `INSERT` may leave the column out
    (nullable, a `DEFAULT`, identity or generated). Identity columns have no
    `DEFAULT`, so `HasDefault` alone misses them.
  - `c.Identity()`: either identity kind.
- **`Catalog.Hash` no longer sorts the catalog in place.** It hashes a sorted
  copy, so it is safe alongside concurrent readers, and the result is the
  same whatever the order of the top-level slices.
- **The hash covers the server's major version only.** `ServerVersion` is left
  out and `ServerVersionNum` is reduced to its major release, so a PostgreSQL
  minor upgrade is no longer reported as drift by `schema check` or the
  watch poll; a major upgrade still is.
- **Functions are ordered deterministically.** `Introspect` orders
  `Catalog.Functions` by schema, name, then input parameter types (fewer
  parameters first, then by type schema and name). Overloads used to come
  back in an unspecified order, so a consumer that keeps the first of
  several overloads could keep a different one from run to run; it now
  always keeps the one with the fewest parameters.
- **`cache.Save` takes a `cache.Writer`** (product name and version; the
  pdbcore version is filled in from the build information), recorded in the
  cache envelope.
- **Typed cache errors.** A cache in another format fails with
  `*cache.FormatVersionError` (with the versions and the writer, when
  recorded); an unreadable or tampered one with an error wrapping
  `cache.ErrCorrupt`. Errors from opening the file are unchanged.
- **PostgreSQL 14 is the minimum.** `introspect.Introspect` fails with
  `*introspect.UnsupportedServerError` on older servers, and so does every
  connection `pgexec.Connect`'s pool opens to one. `RangeType.Multirange` is
  always set by introspection.

### Identity configuration, strict config, typed plugin settings

The products' `rls` section is replaced by three shared `confx` sections,
and configuration loading is strict. Both products use the same keys:

```yaml
auth:
  mode: jwt              # jwt | headers | none
  jwt: {secret, secret_file, jwks_url, jwks_cache_ttl, issuer, audience, leeway, role_claim_path}
  headers: {prefix, claims, role_claim, trusted_proxies}
  mint_type: ""
roles: {switch: true, anonymous: "", default: "", allowed: []}
claims: {prefix: jwt.claims, json_setting: ""}
```

| 0.x key | 1.0 key | Notes |
|---|---|---|
| `rls.enabled: true` | `roles.switch: true` | default |
| `rls.enabled: false` | `roles.switch: false` | credentials are still verified; for no authentication at all also set `auth.mode: none` |
| `rls.anonymous_role` (default `anonymous`) | `roles.anonymous` (default empty) | empty answers requests without credentials with 401 and `WWW-Authenticate`; set it to keep anonymous access |
| `rls.default_role` | `roles.default` | |
| `rls.allowed_roles` | `roles.allowed` | |
| `rls.role_claim` | `auth.jwt.role_claim_path` / `auth.headers.role_claim` | jwt takes a path such as `app_metadata.role` |
| `rls.claims_prefix` (default `pdbq.claims`) | `claims.prefix` (default `jwt.claims`) | set `claims.prefix: pdbq.claims` to keep existing policies, or update them |
| — | `claims.json_setting` | e.g. `request.jwt.claims` for PostgREST/Supabase policies |
| `rls.auth.mode` | `auth.mode` | unknown modes are errors in `auth.New` too |
| `rls.auth.jwt_secret` | `auth.jwt.secret` | or `auth.jwt.secret_file` |
| `rls.auth.jwks_url` | `auth.jwt.jwks_url` | |
| `rls.auth.jwks_cache_ttl` | `auth.jwt.jwks_cache_ttl` | |
| `rls.auth.jwt_issuer` | `auth.jwt.issuer` | |
| `rls.auth.jwt_audience` | `auth.jwt.audience` | |
| — | `auth.jwt.leeway` | default `0s` |
| `rls.auth.header_prefix` | `auth.headers.prefix` | |
| `rls.auth.header_claims` | `auth.headers.claims` | |
| `rls.auth.trusted_proxies` | `auth.headers.trusted_proxies` | required in headers mode (empty no longer trusts every peer) |
| `rls.auth.jwt_type` | `auth.mint_type` | |
| `log.format: <anything>` | `log.format: text` or `json` | other values are rejected |

Go API changes:

- **`auth`**: `New` returns `(*Authenticator, error)` and rejects an unknown
  mode, a jwt mode without a secret or JWKS URL and a headers mode without
  trusted proxies. `Config.Mode` is the typed `auth.Mode`; `Config.Enabled`
  is gone (use `SkipRoleSwitch` to run as the connection role, with
  credentials still verified); `Config.RoleClaim` is split into
  `RoleClaimPath` (jwt) and `HeaderRoleClaim` (headers). An empty
  `AnonymousRole` refuses requests without credentials with
  `ErrAuthRequired`, a credential resolving to no role is
  `ErrRoleNotAllowed`, and a role claim that is not a non-empty string is
  `ErrInvalidToken`. `Challenge(err)` gives the `WWW-Authenticate` value.
  The `Bearer` scheme is case-insensitive.
- **`pgexec`**: `DefaultClaimsPrefix` is gone (`confx.DefaultClaimsPrefix` is
  `jwt.claims`); an empty `Identity.ClaimsPrefix` sets no per-claim settings.
  `Identity.ClaimsJSONSetting` sets every claim as one JSON object. SQLSTATE
  class 21 classifies as `UserInput`.
- **`confx`**: `RLS` and the flat `Auth` fields are replaced by `Auth`,
  `JWT`, `Headers`, `Roles` and `Claims`; `ValidateRLS` by
  `ValidateIdentity(Auth, Roles, Claims)`; `AuthConfig(RLS)` by
  `AuthConfig(Auth, Roles) (auth.Config, error)`. `Plugins` is generic,
  `Plugins[S]`, holding the product's typed settings struct. `Load` rejects
  unknown keys (file keys, and environment variables whose first segment
  names a section) with the key they moved to or the closest valid one;
  products add their own renames with `WithMoved`.
- **`httpx`**: `CORS`, `Preflight` and `CORSWithPreflight` take a
  `CORSPolicy{Origins, AllowMethods, ExposeHeaders}`; CORS and gzip add to
  `Vary` instead of replacing it.
- The test fixture's policies read `jwt.claims.user_id`.
