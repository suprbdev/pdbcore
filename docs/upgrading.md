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
