# specs/cloud-api

Verbatim, gzipped snapshots of each cloud's published API specifications and
the documentation pages that state what the specifications leave out. They
are never edited, and CI reads only these committed copies.

| Directory | Holds | Provenance |
|---|---|---|
| [`aws/`](aws/) | Smithy 2.0 models, AWS guides, published catalogs; [`aws/service-reference/`](aws/service-reference/) the AWS Service Reference | [`aws/SOURCES.md`](aws/SOURCES.md), [`aws/SERVICE_REFERENCE_SOURCES.md`](aws/SERVICE_REFERENCE_SOURCES.md) |
| [`gcp/`](gcp/) | API Discovery documents, Google Cloud documentation pages | [`gcp/SOURCES.md`](gcp/SOURCES.md) |
| [`azure/`](azure/) | OpenAPI (Swagger) documents, Microsoft Learn sources | [`azure/SOURCES.md`](azure/SOURCES.md) |

Each `SOURCES.md` row records the local file, the upstream repository or host,
the exact path, the licence, the pin and the fetch time. A
`<document>.supplement.json` beside a model corrects what the model gets wrong,
with its evidence.

[`docs/specifications.md`](../../docs/specifications.md) explains where each
file comes from, how to vendor or refresh one, what is generated from them, and
every coverage gate that reads them.
