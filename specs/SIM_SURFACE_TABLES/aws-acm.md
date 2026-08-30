# Sim surface — aws-acm

Surface registered in `simulator-aws/acm.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

The extractor reads the route out of a single string literal, so a registration that composes its path from a variable (`"GET "+prefix+"/…"`) produces no row here. Absence from this table is therefore not evidence that an op is unserved — check the source before concluding a gap. The status marker comes from `scripts/classify-sim-handlers.go`, which reads what the handler behind each route actually does.

## Status legend

- ✓ — implemented: the handler reads or writes simulator state, so the operation remembers what it did
- ○ — answers without reaching state. Correct for a published catalog or a computed echo, and the shape a stub has too — read the handler before trusting it
- ? — the handler is not declared in this package, so the generator cannot say
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — NotImplemented on the wire (a declared gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /acm/email-validation/{token}` | ✓ `simulator-aws/acm.go:253::handleACMEmailValidation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.RequestCertificate` | ✓ `simulator-aws/acm.go:255::handleACMRequestCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.DescribeCertificate` | ✓ `simulator-aws/acm.go:256::handleACMDescribeCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.DeleteCertificate` | ✓ `simulator-aws/acm.go:257::handleACMDeleteCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ListCertificates` | ✓ `simulator-aws/acm.go:258::handleACMListCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ListCertificateDomainValidations` | ✓ `simulator-aws/acm.go:259::handleACMListCertificateDomainValidations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.AddTagsToCertificate` | ✓ `simulator-aws/acm.go:260::handleACMAddTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.RemoveTagsFromCertificate` | ✓ `simulator-aws/acm.go:261::handleACMRemoveTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ListTagsForCertificate` | ✓ `simulator-aws/acm.go:262::handleACMListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ImportCertificate` | ✓ `simulator-aws/acm.go:263::handleACMImportCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.UpdateCertificateOptions` | ✓ `simulator-aws/acm.go:264::handleACMUpdateOptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ResendValidationEmail` | ✓ `simulator-aws/acm.go:265::handleACMResendValidationEmail` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.RenewCertificate` | ✓ `simulator-aws/acm.go:266::handleACMRenewCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.GetCertificate` | ✓ `simulator-aws/acm.go:267::handleACMGetCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ExportCertificate` | ✓ `simulator-aws/acm.go:268::handleACMExportCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.RevokeCertificate` | ✓ `simulator-aws/acm.go:269::handleACMRevokeCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.GetAccountConfiguration` | ✓ `simulator-aws/acm.go:270::handleACMGetAccountConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.PutAccountConfiguration` | ✓ `simulator-aws/acm.go:271::handleACMPutAccountConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.SearchCertificates` | ✓ `simulator-aws/acm.go:272::handleACMSearchCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.TagResource` | ✓ `simulator-aws/acm.go:273::handleACMTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.UntagResource` | ✓ `simulator-aws/acm.go:274::handleACMUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ListTagsForResource` | ✓ `simulator-aws/acm.go:275::handleACMListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
