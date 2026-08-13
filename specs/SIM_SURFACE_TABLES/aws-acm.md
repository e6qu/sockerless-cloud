# Sim surface — aws-acm

Surface registered in `simulator-aws/acm.go` (and related files grouped under this table). Rows below are the ops the sim currently registers — extracted by `scripts/seed-surface-tables.sh` from `mux.HandleFunc(...)` calls. ✗ rows for ops not handled by the sim are added when a community-filed issue or audit surfaces them.

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops (extracted from HandleFunc registrations)

| Op (verb + path) | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|
| `GET /acm/email-validation/{token}` | ✓ `simulator-aws/acm.go:257::handleACMEmailValidation` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.RequestCertificate` | ✓ `simulator-aws/acm.go:259::handleACMRequestCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.DescribeCertificate` | ✓ `simulator-aws/acm.go:260::handleACMDescribeCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.DeleteCertificate` | ✓ `simulator-aws/acm.go:261::handleACMDeleteCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ListCertificates` | ✓ `simulator-aws/acm.go:262::handleACMListCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ListCertificateDomainValidations` | ✓ `simulator-aws/acm.go:263::handleACMListCertificateDomainValidations` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.AddTagsToCertificate` | ✓ `simulator-aws/acm.go:264::handleACMAddTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.RemoveTagsFromCertificate` | ✓ `simulator-aws/acm.go:265::handleACMRemoveTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ListTagsForCertificate` | ✓ `simulator-aws/acm.go:266::handleACMListTags` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ImportCertificate` | ✓ `simulator-aws/acm.go:267::handleACMImportCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.UpdateCertificateOptions` | ✓ `simulator-aws/acm.go:268::handleACMUpdateOptions` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ResendValidationEmail` | ✓ `simulator-aws/acm.go:269::handleACMResendValidationEmail` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.RenewCertificate` | ✓ `simulator-aws/acm.go:270::handleACMRenewCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.GetCertificate` | ✓ `simulator-aws/acm.go:271::handleACMGetCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ExportCertificate` | ✓ `simulator-aws/acm.go:272::handleACMExportCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.RevokeCertificate` | ✓ `simulator-aws/acm.go:273::handleACMRevokeCertificate` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.GetAccountConfiguration` | ✓ `simulator-aws/acm.go:274::handleACMGetAccountConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.PutAccountConfiguration` | ✓ `simulator-aws/acm.go:275::handleACMPutAccountConfiguration` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.SearchCertificates` | ✓ `simulator-aws/acm.go:276::handleACMSearchCertificates` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.TagResource` | ✓ `simulator-aws/acm.go:277::handleACMTagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.UntagResource` | ✓ `simulator-aws/acm.go:278::handleACMUntagResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |
| `Action CertificateManager.ListTagsForResource` | ✓ `simulator-aws/acm.go:279::handleACMListTagsForResource` | ✓ (direct; see coverage matrix) | ✓ (direct; see coverage matrix) | n/a | |

## Coverage status

- Row-level SDK/Terraform cells summarize the maintained coverage matrix in `specs/SIM_TEST_COVERAGE_MATRIX.md`; detailed client files and client-family `n/a` decisions live there.
- Missing public-cloud operations that are not registered by the simulator still require a concrete BUG and a row here when discovered by a community issue or periodic audit.

<!-- HAND-WRITTEN BEGIN -->
<!-- HAND-WRITTEN END -->
