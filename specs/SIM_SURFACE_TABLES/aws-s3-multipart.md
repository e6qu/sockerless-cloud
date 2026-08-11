# AWS S3 — multipart upload

Surface: `simulator-aws/s3_subresources.go`. Object-level multipart upload family. Every operation listed dispatches via query-string subresource (`?uploads`, `?uploadId`, `?uploadId&partNumber`) on the `{bucket}/{key...}` route, gated by known-bucket (BUG-1150).

Canonical reference: <https://docs.aws.amazon.com/AmazonS3/latest/API/API_Operations_Amazon_Simple_Storage_Service.html>

## Status legend

- ✓ — implemented + tested
- ✗ — missing (paired with an open BUG or issue; never silent)
- 501 — stubbed NotImplemented (wire-visible gap)
- n/a — no meaningful client/provider surface for this op

## Implemented ops

| Operation | Verb + path | sim handler | sdk-test | tf-test | paged-shape verified | notes |
|---|---|---|---|---|---|---|
| CreateMultipartUpload | `POST /{bucket}/{key}?uploads` | ✓ `s3_subresources.go::handleS3InitiateMultipart` | ✓ `s3_subresources_test.go::TestS3_Multipart` | n/a | n/a | InitiateMultipartUpload — opens the upload, returns UploadId. |
| UploadPart | `PUT /{bucket}/{key}?uploadId&partNumber` | ✓ `s3_subresources.go::handleS3UploadPart` | ✓ same | n/a | n/a | aws-chunked streaming-envelope honored via isAWSChunkedRequest helper. |
| CompleteMultipartUpload | `POST /{bucket}/{key}?uploadId` | ✓ `s3_subresources.go::handleS3CompleteMultipart` | ✓ same | n/a | n/a | Final ETag follows canonical `hex(md5(concat(part_md5_bytes)))-N` convention. |
| AbortMultipartUpload | `DELETE /{bucket}/{key}?uploadId` | ✓ `s3_subresources.go::handleS3AbortMultipart` | ✓ `TestS3_AbortMultipart` | n/a | n/a | |
| ListMultipartUploads | `GET /{bucket}?uploads` | ✓ `s3_subresources.go::handleS3ListMultipartUploads` | ✓ `s3_list_parts_test.go::TestS3_Multipart_ListMultipartUploadsPaginator` + CLI `s3api list-multipart-uploads` | n/a (not exposed by provider; see coverage matrix) | ✓ `ListMultipartUploadsPaginator` with `MaxUploads` pagination | |
| ListParts | `GET /{bucket}/{key}?uploadId` | ✓ `s3_subresources.go::handleS3ListParts` | ✓ `s3_list_parts_test.go::TestS3_Multipart_ListParts` + CLI `s3api list-parts` | n/a | ✓ `ListPartsPaginator` with `MaxParts` pagination | |

## Reopens that produced this table

- Issue [#196](https://github.com/e6qu/sockerless/issues/196) was filed against PR #200 (Phase 176, BUG-1138) and refiled as a reopen because PR #200/#202 covered Initiate / UploadPart / Complete / Abort / ListMultipartUploads but **missed ListParts**. Phase 178 BUG-1148 closes the gap with both the handler and this table; the `surface-table-completeness` skill now keeps every row in lockstep, so the next multipart op the AWS API adds gets a row here before the implementation lands.

## Postmortem trail

- BUG-1138 (Phase 176) — first fix wired the most common multipart ops; happy-path SDK round-trip via `manager.Uploader` worked, never exercised retry path.
- BUG-1142 (Phase 177) — extended bucket-level subresources; reviewed the multipart family at the time but didn't create a table, so the audit fell back to "what the user named in the issue."
- BUG-1148 (Phase 178) — the reopen forced the table into existence. Future multipart gaps surface during table audit, before they ship.
