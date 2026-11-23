# Security policy

pdbcore is the engine behind [pdbq](https://github.com/suprbdev/pdbq) and
[pdbr](https://github.com/suprbdev/pdbr): authentication, request identity
(role and claims) and transaction execution live here. A vulnerability in
those packages affects both products, so report it here or in either product
repository; it is triaged once and fixed in all of them.

## Reporting a vulnerability

Report vulnerabilities privately through GitHub security advisories:
[Report a vulnerability](https://github.com/suprbdev/pdbcore/security/advisories/new)
(the repository's Security tab, "Report a vulnerability"). Do not open a
public issue or pull request for a suspected vulnerability.

Include the pdbcore version (and the pdbq or pdbr version that pins it), the
PostgreSQL version, the relevant configuration (redact secrets) and a
reproduction. A failing test in the affected package is ideal.

pdbcore is maintained by a small team. Response targets, measured from the
report:

| Step | Target |
|---|---|
| Acknowledge the report | 3 working days |
| Triage and confirm or decline, with severity | 7 days |
| Fix or mitigation for a confirmed high or critical issue | 30 days |
| Coordinated disclosure | with the fixed releases, credited if you wish |

These are targets, not guarantees. The reporter is kept informed if one
slips.

## Supported versions

pdbcore is a v0.x module without an external compatibility promise (see
[docs/compatibility.md](docs/compatibility.md)). Only the latest release is
supported: a fix ships as the next patch release and the products bump to it.
Supported PostgreSQL versions are listed in
[docs/compatibility.md](docs/compatibility.md#supported-versions).

## Scope

In scope:

- `auth`: accepting a forged, expired or wrongly signed JWT, JWKS handling,
  trusting claim headers from an untrusted peer.
- `pgexec`: one request's role or claims reaching another request or
  connection, or an identity statement that does not apply.
- `introspect`, `smarttags`, `smartcomments`: exposing objects that smart
  comments or privileges hide.
- `confx`, `httpx`: configuration handling that weakens a security setting
  silently, and HTTP scaffolding such as CORS.
- SQL injection through the `postgis` and `contrib` SQL builders and
  `pgexec` helpers.

Out of scope: findings that need superuser, `BYPASSRLS` or table-owner access
to the database or write access to the configuration, and vulnerabilities in
PostgreSQL, PostGIS, extensions, Go or third-party modules (report those
upstream).

## Verifying releases

Each release publishes a CycloneDX SBOM (`pdbcore_<version>_sbom.cdx.json`)
with a GitHub build provenance attestation:

```console
$ gh attestation verify pdbcore_<version>_sbom.cdx.json --repo suprbdev/pdbcore
```
