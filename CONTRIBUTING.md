# Contributing to pdbcore

pdbcore is the shared engine of [pdbq](https://github.com/suprbdev/pdbq) and
[pdbr](https://github.com/suprbdev/pdbr). Most changes start here and are then
consumed by the products, so read the "Changes that touch both products"
section before opening a pull request.

Security issues go through [SECURITY.md](SECURITY.md), not public issues.

## Development setup

You need Go (the version in `go.mod`), Docker with the Compose plugin for the
database-backed tests, and optionally `golangci-lint` and
[`act`](https://github.com/nektos/act).

```console
$ git clone https://github.com/suprbdev/pdbcore && cd pdbcore
$ make test        # hermetic unit tests, go test -race ./...
$ make lint        # golangci-lint (falls back to go vet)
$ make test-e2e    # DB-backed tests on a disposable PostGIS
$ make help        # every target
```

## Tests

- `make test` needs no database. Packages that need one skip their DB tests
  when `PDBCORE_TEST_DATABASE_URL` is unset.
- `make test-e2e` starts `compose.test.yaml` (project `pdbcore-test`, host
  port 5434, `postgis/postgis:18-3.7`), installs pgvector into the container
  on a best-effort basis, runs the DB-backed packages (`testutil`, `pgexec`,
  `watch`, `introspect`, `postgis`, `contrib`, `metrics`) and tears the
  database down. To run against another PostgreSQL major, change the image tag
  in `compose.test.yaml` locally or point `PDBCORE_TEST_DATABASE_URL` at your
  own database loaded with `make fixture | psql`.
- The shared fixture is `testutil/fixture.sql` with its Go twin
  `testutil.FixtureCatalog()`. `testutil/drift_test.go` loads the SQL, introspects
  it and asserts the result equals the Go value, so the two must change together.
- `introspect/testdata/catalog_format.golden` pins the catalog JSON shape. If it
  fails, either you changed the shape by accident or you must bump
  `introspect.CatalogFormatVersion` and regenerate the golden file in the same
  change.

## Changes that touch both products

The fixture, the catalog model and anything the products compile against are
tested in pdbq and pdbr as well. A fixture change is a pdbcore pull request
first and a golden-file update in both products second. CI's `downstream` job
builds and tests the `main` of pdbq and pdbr against your branch (using a
`replace` directive in a throwaway checkout, never committed); if it fails, the
change breaks a product and needs a coordinated product pull request. Release
order is pdbcore, then pdbq, then pdbr.

## Running CI locally

Run the workflow with `act` before pushing, one job at a time (matrix entries
collide on host port 5434), without pulling images:

```console
$ act pull_request -W .github/workflows/ci.yaml -j lint-test --pull=false
$ act pull_request -W .github/workflows/ci.yaml -j e2e --matrix pg:19 --pull=false
```

Pull requests run PostgreSQL 14 and 19. Pushes to `main` run 14 to 19. Wait
for another `act` run's containers to finish rather than removing them.

## Load and fuzz tests

Anything that starts a server for load, DoS or fuzz probing must run under a
hard memory cap, because an uncapped probe once exhausted a developer machine:

```console
$ systemd-run --user --scope -p MemoryMax=2G -p MemorySwapMax=0 <command>
```

Keep request sizes bounded.

## Commits and pull requests

Use [Conventional Commits](https://www.conventionalcommits.org/) for commit
messages and pull request titles: `type(scope): subject`, with types such as
`feat`, `fix`, `perf`, `refactor`, `test`, `docs`, `ci` and `chore`, and `!` or
a `BREAKING CHANGE:` footer for breaking changes. Merges are squashed and the
squash subject becomes the changelog entry, so make it read as one. Do not put
issue references in commit messages. In Markdown, put code-like `@` words in
backticks so GitHub does not mention an account.

Add a line under "Unreleased" in [CHANGELOG.md](CHANGELOG.md) for anything a
user of the module would notice.

## Releasing

Maintainers run `make release` on an up-to-date `main`: it checks the tree,
asks for `vX.Y.Z`, runs `make test lint`, tags and pushes. The `Release`
workflow tests the tag, generates a CycloneDX SBOM, attests it and publishes the
GitHub release. Then bump pdbq and pdbr (`go get
github.com/suprbdev/pdbcore@vX.Y.Z`), release pdbq, then pdbr. Move the
"Unreleased" notes in `CHANGELOG.md` under the new version before tagging.

Compatibility rules are in [docs/compatibility.md](docs/compatibility.md).
