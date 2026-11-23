# Compatibility and versioning

pdbcore is the engine module for [pdbq](https://github.com/suprbdev/pdbq) and
[pdbr](https://github.com/suprbdev/pdbr). It is not a product and has **no
external compatibility promise**.

## Policy

- pdbcore stays at `v0.x`. A minor bump may change or remove any exported
  identifier, configuration type, catalog field or cache format; a patch bump
  does not add or break API.
- The products pin an exact pdbcore version. Their major versions are not tied
  to pdbcore's: pdbq 1.x and pdbr 1.x may sit on any pdbcore `v0.x`.
- Nothing outside the products is expected to import pdbcore. If you do, pin
  an exact version and read the [changelog](../CHANGELOG.md) before each bump.
  Issues about using pdbcore on its own are welcome but are not covered by a
  stability promise.
- The compatibility promise that does exist is made by the products, for their
  own surfaces (configuration keys, CLI, HTTP, GraphQL and REST grammar, error
  codes, metrics). See pdbq's and pdbr's `docs/compatibility.md`. pdbcore's
  job is not to make those change without the products noticing: CI builds and
  tests pdbq and pdbr at their `main` against every pull request.

## Schema cache format

`introspect.CatalogFormatVersion` identifies the schema cache wire format.
Bumping it is a pdbcore minor release, and both products upgrade in lockstep.
`introspect/testdata/catalog_format.golden` fails the build when the JSON shape
changes without a bump. Within one format version a cache written by pdbq
loads in pdbr and the other way round. After a bump, the products ask you to
re-run `schema dump`.

## Deprecation

Removed or renamed API is called out in the changelog. Go identifiers that are
about to go carry a `// Deprecated:` doc comment for at least one minor
release where that is practical; since the module is v0.x this is a courtesy,
not a promise.

## Supported versions

| Component | Supported |
|---|---|
| PostgreSQL | 14 to 19. Older servers are refused at introspection and on every pooled connection. |
| PostGIS | 3.7 is what CI runs against. Earlier 3.x releases are not tested. |
| pgvector | Best effort. CI installs it when the image's package source has it and skips its tests otherwise; operators added in 0.7 (`l1`, bit distances) need 0.7 or later. |
| Go | The version in `go.mod` (1.26) or newer, for building from source. |

CI runs every PostgreSQL major from 14 to 19 on pushes to `main` and 14 and 19
on pull requests, on Linux amd64. See [../CONTRIBUTING.md](../CONTRIBUTING.md).
