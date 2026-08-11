# Azure Storage — host/path data planes

Surface: `simulator-azure/blob.go`, `simulator-azure/storage_dataplane.go` (dispatch, shares, queues, tables), `simulator-azure/files_dataplane.go` + `simulator-azure/files_entries.go` (Files shares, directories, files, ranges and links), `simulator-azure/files_ranges_*.go` + `simulator-azure/files_punch_*.go` (the filesystem extent map List Ranges and Clear Range act on) and `simulator-azure/queue_dataplane.go` (queue access policies, message updates, Queue service configuration).

These are the service-native Storage REST data planes advertised from `Microsoft.Storage/storageAccounts` as `{account}.blob.<host>`, `{account}.file.<host>`, `{account}.queue.<host>`, and `{account}.table.<host>`. The simulator also supports the Azurite-compatible path-style forms used by SDKs configured with localhost endpoints.

## Status legend

- ✓ — implemented + tested
- ✗ — missing
- n/a — not a canonical client surface for this protocol in the repo harness

## Blob

Every operation of the vendored `storage-dataplane-blob-2026-04-06` Swagger document is served; the coverage gate in `simulator-azure/azure_coverage_test.go` locks the count at 69/69.

| Operation | Verb + path | sim handler | sdk-test | raw-wire test | paged-shape verified | notes |
|---|---|---|---|---|---|---|
| CreateContainer | `PUT /{container}?restype=container` | ✓ `handleCreateContainer` | ✓ `TestStorageSDK_BlobLifecycleAndPagedLists` | ✓ `TestBlobDataPlane_RoundTrip` | n/a | Records the container version Restore Container addresses. |
| GetContainerProperties | `GET/HEAD /{container}?restype=container` | ✓ `handleGetContainer` | ✓ `TestStorageSDK_BlobContainerAndBlobProperties` | ✓ `TestBlobDataPlane_ListContainersProperties` | n/a | Emits lease state/status/duration and the public-access level. |
| DeleteContainer | `DELETE /{container}?restype=container` | ✓ `handleDeleteContainer` | ✓ `TestStorageSDK_BlobLifecycleAndPagedLists` | ✓ `TestBlobDataPlane_RoundTrip` | n/a | Cascades blobs; retains the container when the ARM container delete-retention policy is on. |
| SetContainerMetadata | `PUT /{container}?restype=container&comp=metadata` | ✓ `handleSetContainerMetadata` | ✓ `TestStorageSDK_ContainerMetadataAndAccessPolicy` | ✓ `TestBlobContainerLeaseAndPolicyCLI` | n/a | |
| GetContainerAccessPolicy | `GET /{container}?restype=container&comp=acl` | ✓ `handleGetContainerAccessPolicy` | ✓ `TestStorageSDK_ContainerMetadataAndAccessPolicy` | ✓ `TestBlobContainerLeaseAndPolicyCLI` | n/a | Real signed-identifier XML. |
| SetContainerAccessPolicy | `PUT /{container}?restype=container&comp=acl` | ✓ `handleSetContainerAccessPolicy` | ✓ `TestStorageSDK_ContainerMetadataAndAccessPolicy` | ✓ `TestBlobContainerLeaseAndPolicyCLI` | n/a | An empty document clears every policy. |
| RestoreContainer | `PUT /{container}?restype=container&comp=undelete` | ✓ `handleRestoreContainer` | ✓ `TestStorageSDK_ContainerSoftDeleteAndRestore` | n/a | n/a | Governed by the ARM `blobServices/default` container delete-retention policy. |
| RenameContainer | `PUT /{container}?restype=container&comp=rename` | ✓ `handleRenameContainer` | ✓ `TestStorageSDK_ContainerRename` | n/a | n/a | The azblob module generates but does not surface the operation, so the SDK test issues it at the specification's coordinate. |
| ContainerLease (acquire/renew/change/release/break) | `PUT /{container}?restype=container&comp=lease` | ✓ `handleContainerLease` | ✓ `TestStorageSDK_ContainerLeaseLifecycleAndEnforcement` | ✓ `TestBlobContainerLeaseAndPolicyCLI` | n/a | Enforced: a delete without the lease ID fails 412 `LeaseIdMissing`. |
| ContainerSubmitBatch | `POST /{container}?restype=container&comp=batch` | ✓ `handleBlobSubmitBatch` | ✓ `TestStorageSDK_BlobBatchDeleteAndSetTier` | n/a | n/a | Sub-requests dispatch through the simulator's own blob handler. |
| ContainerFilterBlobs | `GET /{container}?restype=container&comp=blobs` | ✓ `handleFilterBlobs` | ✓ `TestStorageSDK_BlobSetExpiryAndTags` | ✓ `TestBlobTagFilterExpressionIsEvaluated` | ✓ SDK marker | Tag expressions are parsed and evaluated; unsupported grammar is refused. |
| ContainerGetAccountInfo | `GET /{container}?restype=account&comp=properties` | ✓ `handleBlobGetAccountInfo` | ✓ `TestStorageSDK_BlobServiceStatisticsAndAccountInfo` | n/a | n/a | |
| ListContainers | `GET /?comp=list` | ✓ `handleListContainers` | ✓ `TestStorageSDK_BlobLifecycleAndPagedLists` | ✓ `TestBlobDataPlane_ListContainersProperties` | ✓ SDK pager | Honors `include=metadata,deleted`; reports lease and retention. |
| ListBlobs (flat + hierarchy) | `GET /{container}?restype=container&comp=list` | ✓ `handleListBlobs` | ✓ `TestStorageSDK_BlobLifecycleAndPagedLists` | ✓ `TestBlobDataPlane_RoundTrip` | ✓ SDK pager | Honors `include=snapshots,deleted,metadata,tags`; emits the full property set. |
| PutBlob (block/page/append) | `PUT /{container}/{blob}` | ✓ `handlePutBlob` | ✓ `TestStorageSDK_BlobLifecycleAndPagedLists`, `TestStorageSDK_PageBlobRangesAndResize`, `TestStorageSDK_AppendBlobBlocksAndSeal` | ✓ `TestBlobDataPlane_RoundTrip` | n/a | Page blobs allocate the declared size sparsely; append blobs start empty. |
| CopyBlob / CopyBlobFromURL | `PUT /{container}/{blob}` + `x-ms-copy-source`, also `?comp=copy` | ✓ `handleCopyBlob`, `handleBlobCompCopy` | ✓ `TestStorageSDK_BlobCopyFromURLAndAbort` | ✓ `TestBlobCopyCLI` | n/a | Copies stored bytes from host-style or path-style source URLs, snapshot sources included. |
| AbortCopyBlob | `PUT /{container}/{blob}?comp=copy&copyid=…` | ✓ `handleAbortCopyBlob` | ✓ `TestStorageSDK_BlobCopyFromURLAndAbort` | n/a | n/a | A completed copy answers `NoPendingCopyOperation`, as Azure does. |
| IncrementalCopyBlob | `PUT /{container}/{blob}?comp=incrementalcopy` | ✓ `handlePageBlobCopyIncremental` | ✓ `TestStorageSDK_PageBlobIncrementalCopy` | n/a | n/a | Requires a snapshot source; produces a destination snapshot. |
| StageBlock | `PUT /{container}/{blob}?comp=block&blockid=…` | ✓ `handleStageBlock` | ✓ `TestStorageSDK_BlobBlockStaging` | ✓ `TestBlobBlockStagingCLI` | n/a | |
| StageBlockFromURL | `PUT /{container}/{blob}?comp=block` + `x-ms-copy-source` | ✓ `handleStageBlockFromURL` | ✓ `TestStorageSDK_BlockBlobStageBlockFromURL` | n/a | n/a | Stages the source range's bytes. |
| CommitBlockList | `PUT /{container}/{blob}?comp=blocklist` | ✓ `handleCommitBlockList` | ✓ `TestStorageSDK_BlobBlockStaging` | ✓ `TestBlobBlockStagingCLI` | n/a | |
| GetBlockList | `GET /{container}/{blob}?comp=blocklist` | ✓ `handleGetBlockList` | ✓ `TestStorageSDK_BlobBlockStaging` | ✓ `TestBlobBlockStagingCLI` | n/a | |
| GetBlob | `GET /{container}/{blob}` | ✓ `handleGetBlob` | ✓ `TestStorageSDK_BlobLifecycleAndPagedLists` | ✓ `TestBlobDataPlane_RoundTrip` | n/a | Honors `?snapshot=`. |
| GetBlobProperties | `HEAD /{container}/{blob}` | ✓ `handleHeadBlob` | ✓ `TestStorageSDK_BlobContainerAndBlobProperties` | ✓ `TestBlobDataPlane_RoundTrip` | n/a | Emits lease, tier, tags count, seal, sequence number, immutability and expiry headers. |
| DeleteBlob | `DELETE /{container}/{blob}` | ✓ `handleDeleteBlob` | ✓ `TestStorageSDK_BlobLifecycleAndPagedLists` | ✓ `TestBlobDataPlane_RoundTrip` | n/a | Honors `x-ms-delete-snapshots`; soft-deletes when the delete-retention policy is on. |
| UndeleteBlob | `PUT /{container}/{blob}?comp=undelete` | ✓ `handleUndeleteBlob` | ✓ `TestStorageSDK_BlobSoftDeleteAndUndelete` | ✓ `TestBlobSnapshotSoftDeleteAndUndeleteCLI` | n/a | |
| CreateSnapshot | `PUT /{container}/{blob}?comp=snapshot` | ✓ `handleCreateBlobSnapshot` | ✓ `TestStorageSDK_BlobSnapshotsAreAddressableCopies` | ✓ `TestBlobSnapshotSoftDeleteAndUndeleteCLI` | n/a | Real copies addressable by `?snapshot=<ts>`. |
| SetBlobMetadata | `PUT /{container}/{blob}?comp=metadata` | ✓ `handleSetBlobMetadata` | ✓ `TestStorageSDK_BlobSystemPropertiesAndTier` | ✓ `TestBlobTierMetadataAndPropertiesCLI` | n/a | |
| SetBlobHTTPHeaders | `PUT /{container}/{blob}?comp=properties` | ✓ `handleSetBlobHTTPHeaders` | ✓ `TestStorageSDK_BlobSystemPropertiesAndTier` | n/a | n/a | Discriminated from Resize / Update Sequence Number by the `x-ms-blob-*` headers. |
| SetBlobTier | `PUT /{container}/{blob}?comp=tier` | ✓ `handleSetBlobTier` | ✓ `TestStorageSDK_BlobSystemPropertiesAndTier` | ✓ `TestBlobTierMetadataAndPropertiesCLI` | n/a | An unknown tier is refused. |
| SetBlobExpiry | `PUT /{container}/{blob}?comp=expiry` | ✓ `handleSetBlobExpiry` | ✓ `TestStorageSDK_BlobSetExpiryAndTags` | n/a | n/a | All four expiry options. |
| GetBlobTags / SetBlobTags | `GET/PUT /{container}/{blob}?comp=tags` | ✓ `handleGetBlobTags`, `handleSetBlobTags` | ✓ `TestStorageSDK_BlobSetExpiryAndTags` | ✓ `TestBlobTagFilterExpressionIsEvaluated` | n/a | Tag count surfaces on properties and drives Find Blobs by Tags. |
| SetImmutabilityPolicy / DeleteImmutabilityPolicy | `PUT/DELETE /{container}/{blob}?comp=immutabilityPolicies` | ✓ `handleSetBlobImmutabilityPolicy`, `handleDeleteBlobImmutabilityPolicy` | ✓ `TestStorageSDK_BlobImmutabilityPolicyAndLegalHold` | ✓ `TestBlobLegalHoldAndImmutabilityCLI` | n/a | A locked policy refuses delete and removal until it expires. |
| SetBlobLegalHold | `PUT /{container}/{blob}?comp=legalhold` | ✓ `handleSetBlobLegalHold` | ✓ `TestStorageSDK_BlobImmutabilityPolicyAndLegalHold` | ✓ `TestBlobLegalHoldAndImmutabilityCLI` | n/a | A held blob refuses delete and overwrite. |
| BlobLease (acquire/renew/change/release/break) | `PUT /{container}/{blob}?comp=lease` | ✓ `handleBlobLease` | ✓ `TestStorageSDK_BlobLeaseLifecycleAndEnforcement`, `TestStorageSDK_BlobLeaseBreakAndFiniteExpiry` | ✓ `TestBlobLeaseCLI`, `TestBlobExpiredLeaseNoLongerBlocksWrites` | n/a | Finite leases really expire; writes without the lease ID fail 412. |
| UploadPages / ClearPages | `PUT /{container}/{blob}?comp=page` | ✓ `handlePageBlobUploadPages`, `handlePageBlobClearPages` | ✓ `TestStorageSDK_PageBlobRangesAndResize` | n/a | n/a | 512-byte alignment enforced; sparse ranges tracked. |
| UploadPagesFromURL | `PUT /{container}/{blob}?comp=page` + `x-ms-copy-source` | ✓ `handlePageBlobUploadPagesFromURL` | ✓ `TestStorageSDK_PageBlobUploadPagesFromURL` | n/a | n/a | |
| GetPageRanges / GetPageRangesDiff | `GET /{container}/{blob}?comp=pagelist` | ✓ `handleGetPageRanges` | ✓ `TestStorageSDK_PageBlobRangesAndResize` | ✓ `TestBlobDataPlaneStateSurvivesRestart` | n/a | `prevsnapshot` yields the written/cleared diff against a snapshot. |
| PageBlobResize | `PUT /{container}/{blob}?comp=properties` + `x-ms-blob-content-length` | ✓ `handlePageBlobResize` | ✓ `TestStorageSDK_PageBlobRangesAndResize` | n/a | n/a | |
| PageBlobUpdateSequenceNumber | `PUT /{container}/{blob}?comp=properties` + `x-ms-sequence-number-action` | ✓ `handlePageBlobUpdateSequenceNumber` | ✓ `TestStorageSDK_PageBlobRangesAndResize` | n/a | n/a | increment / max / update. |
| AppendBlock / AppendBlockFromURL | `PUT /{container}/{blob}?comp=appendblock` | ✓ `handleAppendBlock`, `handleAppendBlockFromURL` | ✓ `TestStorageSDK_AppendBlobBlocksAndSeal` | n/a | n/a | Real append offset and committed-block count; append-position and max-size preconditions enforced. |
| SealAppendBlob | `PUT /{container}/{blob}?comp=seal` | ✓ `handleAppendBlobSeal` | ✓ `TestStorageSDK_AppendBlobBlocksAndSeal` | n/a | n/a | A sealed blob refuses further appends. |
| QueryBlobContents | `POST /{container}/{blob}?comp=query` | ✓ `handleBlobQuery` | ✓ `TestStorageSDK_BlobQuery` | ✓ `TestBlobQueryCLI` | n/a | Real query execution over the stored bytes, Avro-framed response. Grammar boundary below. |
| BlobGetAccountInfo | `GET /{container}/{blob}?restype=account&comp=properties` | ✓ `handleBlobGetAccountInfo` | ✓ `TestStorageSDK_BlobServiceStatisticsAndAccountInfo` | n/a | n/a | |
| GetBlobServiceProperties | `GET/HEAD /?restype=service&comp=properties` | ✓ `handleGetBlobServiceProperties` | ✓ `TestStorageSDK_BlobSoftDeleteAndUndelete` | ✓ `TestBlobSnapshotSoftDeleteAndUndeleteCLI` | n/a | Always returns the complete document. |
| SetBlobServiceProperties | `PUT /?restype=service&comp=properties` | ✓ `handleSetBlobServiceProperties` | ✓ `TestStorageSDK_BlobSoftDeleteAndUndelete` | ✓ `TestBlobSnapshotSoftDeleteAndUndeleteCLI` | n/a | Merges: a section the request omits keeps its current setting. |
| GetBlobServiceStatistics | `GET /?restype=service&comp=stats` | ✓ `handleGetBlobServiceStatistics` | ✓ `TestStorageSDK_BlobServiceStatisticsAndAccountInfo` | n/a | n/a | |
| GetUserDelegationKey | `POST /?restype=service&comp=userdelegationkey` | ✓ `handleGetUserDelegationKey` | ✓ `TestStorageSDK_BlobUserDelegationKey` | n/a | n/a | OAuth-only; identity claims come from the Microsoft Entra token, and the issued key is retained. |
| ServiceGetAccountInfo | `GET /?restype=account&comp=properties` | ✓ `handleBlobGetAccountInfo` | ✓ `TestStorageSDK_BlobServiceStatisticsAndAccountInfo` | n/a | n/a | |
| ServiceFilterBlobs | `GET /?comp=blobs` | ✓ `handleFilterBlobs` | ✓ `TestStorageSDK_BlobSetExpiryAndTags` | ✓ `TestBlobTagFilterExpressionIsEvaluated` | ✓ SDK marker | Account-wide tag search. |
| ServiceSubmitBatch | `POST /?comp=batch` | ✓ `handleBlobSubmitBatch` | ✓ `TestStorageSDK_BlobBatchDeleteAndSetTier` | n/a | n/a | Real multipart parsing, dispatch and response assembly. |

### Query Blob Contents — grammar boundary

`handleBlobQuery` executes the SQL subset the official SDKs emit:
`SELECT * FROM BlobStorage [WHERE <predicate>]` and
`SELECT <column>[, <column>…] FROM BlobStorage [WHERE <predicate>]`, where a
column is a positional `_N` reference or a header name and a predicate is a
conjunction *or* a disjunction of `<column> <op> <literal>` comparisons with op
one of `=`, `<>`, `!=`, `>`, `>=`, `<`, `<=`. Delimited and JSON-lines input and
output serializations are supported. Anything outside that — aggregates,
function calls, parenthesised or mixed AND/OR predicates, a non-`BlobStorage`
source, the Apache Arrow output format — is refused with a `ParseError` naming
the construct, never answered with rows the query did not ask for.

## Files

Every one of the 51 operations in `storage-dataplane-file-2026-04-06` is served. A share's bytes live in a real directory tree under `<SIM_DATA_DIR>/files/<account>/<share>` — the directory a Container Apps / Azure Functions workload bind-mounts for a `Volume{StorageType: AzureFile}` — so directories, files, hard links and symbolic links are real filesystem objects. What a POSIX tree cannot express (user metadata, leases, security descriptors, snapshot identity, soft-delete state) lives in the simulator's persistent stores and survives a restart.

| Operation | Verb + path | sim handler | sdk-test | raw-wire test | paged-shape verified | notes |
|---|---|---|---|---|---|---|
| Service_ListSharesSegment | `GET /?comp=list` | ✓ `handleFilesListShares` | ✓ `TestStorageSDK_FileLifecycleAndPagedLists`, `TestFilesSDK_ShareSnapshotFreezesContents` | ✓ `TestFilesShareListCLI`, `TestFilesDirectoryMetadataAndShareOperationsCLI` | ✓ SDK pager | Honors `prefix`, `marker`, `maxresults`, `include=snapshots`, `include=deleted`. |
| Service_GetProperties | `GET/HEAD /?restype=service&comp=properties` | ✓ `handleFilesGetServiceProperties` | ✓ `TestFilesSDK_ServicePropertiesAndUserDelegationKey` | ✓ `TestFilesDataPlaneServedOperations` | n/a | Reads back what Set Service Properties wrote. |
| Service_SetProperties | `PUT /?restype=service&comp=properties` | ✓ `handleFilesSetServiceProperties` | ✓ `TestFilesSDK_ServicePropertiesAndUserDelegationKey` | ✓ `TestFilesDataPlaneServedOperations` | n/a | |
| Service_GetUserDelegationKey | `POST /?restype=service&comp=userdelegationkey` | ✓ `handleFilesGetUserDelegationKey` | ✓ `TestFilesSDK_ServicePropertiesAndUserDelegationKey` | ✓ `TestFilesDataPlaneServedOperations` | n/a | Key value derived from the account, tenant and requested window. |
| Share_Create | `PUT /{share}?restype=share` | ✓ `handleFilesCreateShare` | ✓ `TestStorageSDK_FileLifecycleAndPagedLists` | ✓ `TestFilesDataPlane` | n/a | |
| Share_GetProperties | `GET/HEAD /{share}?restype=share` | ✓ `handleFilesGetShareProperties` | ✓ `TestStorageSDK_FileShareAndFileProperties`, `TestFilesSDK_ShareLeaseGuardsWrites` | ✓ `TestFilesDataPlane` | n/a | Reports quota, access tier and lease state; honors `sharesnapshot`. |
| Share_Delete | `DELETE /{share}?restype=share` | ✓ `handleFilesDeleteShare` | ✓ `TestStorageSDK_FileLifecycleAndPagedLists`, `TestFilesSDK_ShareSoftDeleteAndRestore` | ✓ `TestFilesDataPlane` | n/a | Cascades files, snapshots, permissions and leases; soft-deletes when the account's `shareDeleteRetentionPolicy` is on; deletes only the snapshot when `sharesnapshot` is given. |
| Share_AcquireLease / ReleaseLease / ChangeLease / RenewLease / BreakLease | `PUT /{share}?restype=share&comp=lease` | ✓ `filesHandleLease` | ✓ `TestFilesSDK_ShareLeaseGuardsWrites` | ✓ `TestFilesDataPlaneLeaseGuardsWrites` | n/a | Action from `x-ms-lease-action`; a leased share refuses a write without the lease id (`LeaseIdMissing` / `LeaseIdMismatchWithShareOperation`, 412). |
| Share_CreateSnapshot | `PUT /{share}?restype=share&comp=snapshot` | ✓ `handleFilesCreateShareSnapshot` | ✓ `TestFilesSDK_ShareSnapshotFreezesContents` | ✓ `TestFilesDirectoryMetadataAndShareOperationsCLI` | n/a | Copies the share's directory tree; `?sharesnapshot=` reads the frozen bytes. |
| Share_SetProperties | `PUT /{share}?restype=share&comp=properties` | ✓ `handleFilesSetShareProperties` | ✓ `TestFilesSDK_SharePermissionsStatisticsAndProperties` | ✓ `TestFilesDirectoryMetadataAndShareOperationsCLI` | n/a | Quota, access tier, root squash. |
| Share_SetMetadata | `PUT /{share}?restype=share&comp=metadata` | ✓ `handleFilesSetShareMetadata` | ✓ `TestFilesSDK_ShareLeaseGuardsWrites` | ✓ `TestAzureStorageDataPlaneStateSurvivesSimulatorRestart_SDK` | n/a | |
| Share_GetAccessPolicy / SetAccessPolicy | `GET/PUT /{share}?restype=share&comp=acl` | ✓ `handleFilesShareACL` | ✓ `TestStorageSDK_FileShareStoredAccessPolicy` | ✓ `TestFilesDataPlaneServedOperations` | n/a | Mirrored onto the ARM share's `signedIdentifiers`. |
| Share_CreatePermission / GetPermission | `PUT/GET /{share}?restype=share&comp=filepermission` | ✓ `handleFilesCreatePermission` / `handleFilesGetPermission` | ✓ `TestFilesSDK_SharePermissionsStatisticsAndProperties` | ✓ `TestAzureStorageDataPlaneStateSurvivesSimulatorRestart_SDK` | n/a | Key derived from the descriptor, so an identical descriptor always maps to the same key. |
| Share_GetStatistics | `GET /{share}?restype=share&comp=stats` | ✓ `handleFilesGetShareStatistics` | ✓ `TestFilesSDK_SharePermissionsStatisticsAndProperties` | ✓ `TestFilesDirectoryMetadataAndShareOperationsCLI` | n/a | Walks the share's tree and reports the bytes it actually holds. |
| Share_Restore | `PUT /{share}?restype=share&comp=undelete` | ✓ `handleFilesRestoreShare` | ✓ `TestFilesSDK_ShareSoftDeleteAndRestore` | n/a | n/a | Restores a share Delete Share soft-deleted under the account's `shareDeleteRetentionPolicy`, with its contents. |
| Directory_Create | `PUT /{share}/{dir}?restype=directory` | ✓ `handleFilesCreateDirectory` | ✓ `TestFilesSDK_NestedDirectoryTreeEndToEnd` | ✓ `TestFilesNestedDirectoryTreeCLI` | n/a | Real `mkdir` at any depth; `ParentNotFound` when the parent does not exist. Terraform: `azurerm_storage_share_directory` in `terraform-tests/main.tf`. |
| Directory_GetProperties | `GET/HEAD /{share}/{dir}?restype=directory` | ✓ `handleFilesGetDirectoryProperties` | ✓ `TestFilesSDK_NestedDirectoryTreeEndToEnd` | ✓ `TestFilesNestedDirectoryTreeCLI` | n/a | Terraform refreshes `azurerm_storage_share_directory` through it on every plan, so the harness's idempotency plan covers it. |
| Directory_Delete | `DELETE /{share}/{dir}?restype=directory` | ✓ `handleFilesDeleteDirectory` | ✓ `TestFilesSDK_NestedDirectoryTreeEndToEnd` | ✓ `TestFilesNestedDirectoryTreeCLI` | n/a | `DirectoryNotEmpty` (409) for a directory holding anything. Terraform destroys `azurerm_storage_share_directory` through it. |
| Directory_SetMetadata | `PUT /{share}/{dir}?restype=directory&comp=metadata` | ✓ `handleFilesSetDirectoryMetadata` | ✓ `TestFilesSDK_NestedDirectoryTreeEndToEnd` | ✓ `TestFilesDirectoryMetadataAndShareOperationsCLI` | n/a | |
| Directory_SetProperties | `PUT /{share}/{dir}?restype=directory&comp=properties` | ✓ `handleFilesSetDirectoryProperties` | ✓ `TestFilesSDK_NestedDirectoryTreeEndToEnd` | ✓ `TestFilesDataPlaneNestedPathsAreReal` | n/a | `x-ms-file-last-write-time` moves the directory's modification time. |
| Directory_Rename | `PUT /{share}/{dir}?restype=directory&comp=rename` | ✓ `handleFilesRenameEntry` | ✓ `TestFilesSDK_NestedDirectoryTreeEndToEnd` | ✓ `TestFilesDataPlaneNestedPathsAreReal` | n/a | Real `rename`; carries the metadata, permission and lease rows of everything under it. |
| Directory_ListFilesAndDirectoriesSegment | `GET /{share}/{dir}?restype=directory&comp=list` | ✓ `handleFilesListDirectory` | ✓ `TestFilesSDK_DirectoryListingPagesWithPrefixAndMarker` | ✓ `TestFilesNestedDirectoryTreeCLI` | ✓ SDK pager | Any depth, including the share root; honors `prefix`, `marker`, `maxresults` and `sharesnapshot`. |
| Directory_ListHandles / ForceCloseHandles | `GET /{share}/{dir}?comp=listhandles`, `PUT …?comp=forceclosehandles` | ✓ `handleFilesListHandles` / `handleFilesForceCloseHandles` | ✓ `TestFilesSDK_FilePropertiesRangesAndHandles` | n/a | n/a | A handle is an open SMB/NFS session; no such session can exist against a simulator share, so the enumeration is empty and the closed count is 0. |
| File_Create | `PUT /{share}/{dir}/{file}` | ✓ `handleFilesCreateFile` | ✓ `TestStorageSDK_FileLifecycleAndPagedLists` | ✓ `TestFilesDataPlane` | n/a | Allocates the size `x-ms-content-length` declares. |
| File_Download | `GET /{share}/{dir}/{file}` | ✓ `handleFilesGetFile` | ✓ `TestStorageSDK_FileLifecycleAndPagedLists` | ✓ `TestFilesDataPlane` | n/a | Ranged reads answer 206 with `Content-Range`; honors `sharesnapshot`. |
| File_GetProperties | `HEAD /{share}/{dir}/{file}` | ✓ `handleFilesHeadFile` | ✓ `TestStorageSDK_FileShareAndFileProperties` | ✓ `TestFilesDataPlane` | n/a | Reports lease state and the SMB property headers. |
| File_Delete | `DELETE /{share}/{dir}/{file}` | ✓ `handleFilesDeleteFile` | ✓ `TestStorageSDK_FileLifecycleAndPagedLists` | ✓ `TestFilesDataPlane` | n/a | |
| File_SetHTTPHeaders | `PUT /{share}/{dir}/{file}?comp=properties` | ✓ `handleFilesSetFileHTTPHeaders` | ✓ `TestFilesSDK_FilePropertiesRangesAndHandles` | ✓ `TestFilesDataPlaneServedOperations` | n/a | Also the resize operation: `x-ms-content-length` truncates or extends the file. |
| File_SetMetadata | `PUT /{share}/{dir}/{file}?comp=metadata` | ✓ `handleFilesSetFileMetadata` | ✓ `TestFilesSDK_FilePropertiesRangesAndHandles` | ✓ `TestFilesDataPlaneServedOperations` | n/a | |
| File_AcquireLease / ReleaseLease / ChangeLease / BreakLease | `PUT /{share}/{dir}/{file}?comp=lease` | ✓ `handleFilesFileLease` | ✓ `TestFilesSDK_FileLeaseGuardsWrites` | ✓ `TestFilesDataPlaneLeaseGuardsWrites` | n/a | A leased file refuses a write without the lease id (`LeaseIdMissing` / `LeaseIdMismatchWithFileOperation`, 412). |
| File_UploadRange | `PUT /{share}/{dir}/{file}?comp=range` | ✓ `handleFilesUploadRange` | ✓ `TestStorageSDK_FileLifecycleAndPagedLists`, `TestFilesSDK_ClearRangeDeallocatesTheRange` | ✓ `TestFilesDataPlane` | n/a | `x-ms-write: clear` deallocates the range (`filesClearFileRange` → `filePunchHole`), so List Ranges stops reporting it; a range past the allocated size is `InvalidRange`. |
| File_UploadRangeFromURL | `PUT /{share}/{dir}/{file}?comp=range` + `x-ms-copy-source` | ✓ `handleFilesUploadRangeFromURL` | ✓ `TestFilesSDK_CopyAndUploadRangeFromURL` | n/a | n/a | Copies the source range out of the file the URL names. |
| File_GetRangeList | `GET /{share}/{dir}/{file}?comp=rangelist` | ✓ `handleFilesGetRangeList` | ✓ `TestFilesSDK_FilePropertiesRangesAndHandles`, `TestFilesSDK_ClearRangeDeallocatesTheRange` | n/a | n/a | Ranges come from the filesystem's own record of which extents hold data (SEEK_DATA / SEEK_HOLE), so a freshly allocated file lists none and a cleared range disappears. |
| File_StartCopy | `PUT /{share}/{dir}/{file}` + `x-ms-copy-source` | ✓ `handleFilesStartCopy` | ✓ `TestFilesSDK_CopyAndUploadRangeFromURL` | n/a | n/a | The copy completes within the request, so the response reports `x-ms-copy-status: success`. |
| File_AbortCopy | `PUT /{share}/{dir}/{file}?comp=copy&copyid=…` | ✓ `handleFilesAbortCopy` | ✓ `TestFilesSDK_CopyAndUploadRangeFromURL` | n/a | n/a | No copy is ever pending, so it answers `NoPendingCopyOperation` (409). |
| File_ListHandles / ForceCloseHandles | `GET /{share}/{dir}/{file}?comp=listhandles`, `PUT …?comp=forceclosehandles` | ✓ `handleFilesListHandles` / `handleFilesForceCloseHandles` | ✓ `TestFilesSDK_FilePropertiesRangesAndHandles` | n/a | n/a | Same as the directory form: no Files session can be open, so the true enumeration is empty. |
| File_Rename | `PUT /{share}/{dir}/{file}?comp=rename` | ✓ `handleFilesRenameEntry` | ✓ `TestFilesSDK_NestedDirectoryTreeEndToEnd` | ✓ `TestFilesDataPlaneNestedPathsAreReal` | n/a | Real `rename`; the destination is the request path and the source is `x-ms-file-rename-source`. |
| File_CreateHardLink | `PUT /{share}/{dir}/{file}?restype=hardlink` | ✓ `handleFilesCreateHardLink` | ✓ `TestFilesSDK_HardAndSymbolicLinks` | n/a | n/a | Real `link`: one inode, two names, `x-ms-link-count` from the filesystem. |
| File_CreateSymbolicLink / GetSymbolicLink | `PUT/GET /{share}/{dir}/{file}?restype=symboliclink` | ✓ `handleFilesCreateSymbolicLink` / `handleFilesGetSymbolicLink` | ✓ `TestFilesSDK_HardAndSymbolicLinks` | n/a | n/a | Real `symlink`; link text that would resolve outside the share is refused. |

## Queues

Every one of the 16 operations in `storage-dataplane-queue-2018-03-28` is served.

| Operation | Verb + path | sim handler | sdk-test | raw-wire test | paged-shape verified | notes |
|---|---|---|---|---|---|---|
| Queue_Create | `PUT /{queue}` | ✓ `handleQueueCreate` | ✓ `TestStorageSDK_QueueLifecycleAndPagedLists` | ✓ `TestQueuesDataPlane` | n/a | |
| Queue_GetProperties | `GET/HEAD /{queue}?comp=metadata` | ✓ `handleQueueGetMetadata` | ✓ `TestStorageSDK_QueueMetadataPeekAndClear` | ✓ `TestQueuesDataPlane` | n/a | |
| Queue_SetMetadata | `PUT /{queue}?comp=metadata` | ✓ `handleQueueSetMetadata` | ✓ `TestStorageDataPlane_UnservedOperationsDeclareGaps` | ✓ `TestQueuesDataPlaneServedOperations` | n/a | |
| Queue_Delete | `DELETE /{queue}` | ✓ `handleQueueDelete` | ✓ `TestStorageSDK_QueueLifecycleAndPagedLists` | ✓ `TestQueuesDataPlane` | n/a | |
| Queue_GetAccessPolicy / SetAccessPolicy | `GET/PUT /{queue}?comp=acl` | ✓ `handleQueueACL` | ✓ `TestQueueSDK_AccessPolicyRoundTrip` | ✓ `TestQueueAccessPolicyCLI` | n/a | Signed identifiers with start, expiry and permission. |
| Service_ListQueuesSegment | `GET /?comp=list` | ✓ `handleQueuesList` | ✓ `TestStorageSDK_QueueLifecycleAndPagedLists` | ✓ `TestQueueListAndClearMessagesCLI` | ✓ SDK pager | |
| Service_GetProperties | `GET/HEAD /?restype=service&comp=properties` | ✓ `handleQueuesGetServiceProperties` | ✓ `TestQueueSDK_ServicePropertiesAndStatistics` | ✓ `TestQueuesDataPlaneServedOperations` | n/a | Reads back what Set Service Properties wrote. |
| Service_SetProperties | `PUT /?restype=service&comp=properties` | ✓ `handleQueuesSetServiceProperties` | ✓ `TestQueueSDK_ServicePropertiesAndStatistics` | ✓ `TestQueuesDataPlaneAccessPolicyAndServiceProperties` | n/a | |
| Service_GetStatistics | `GET /?restype=service&comp=stats` | ✓ `handleQueuesGetServiceStatistics` | ✓ `TestQueueSDK_ServicePropertiesAndStatistics` | ✓ `TestQueuesDataPlaneAccessPolicyAndServiceProperties` | n/a | One replica holds an account's queues, so replication is `live` and the last sync time is now. |
| Messages_Enqueue | `POST /{queue}/messages` | ✓ `handleQueuePutMessage` | ✓ `TestStorageSDK_QueueLifecycleAndPagedLists` | ✓ `TestQueuesDataPlane` | n/a | |
| Messages_Dequeue | `GET /{queue}/messages` | ✓ `handleQueueGetMessages` | ✓ `TestStorageSDK_QueueLifecycleAndPagedLists` | ✓ `TestQueuesDataPlane` | n/a | Honors `visibilitytimeout` and `numofmessages`. |
| Messages_Peek | `GET /{queue}/messages?peekonly=true` | ✓ `handleQueuePeekMessages` | ✓ `TestStorageSDK_QueueMetadataPeekAndClear` | ✓ `TestQueuesDataPlane` | n/a | |
| Messages_Clear | `DELETE /{queue}/messages` | ✓ `handleQueueClearMessages` | ✓ `TestStorageSDK_QueueMetadataPeekAndClear` | ✓ `TestQueueListAndClearMessagesCLI` | n/a | |
| MessageId_Update | `PUT /{queue}/messages/{messageid}?popreceipt=…&visibilitytimeout=…` | ✓ `handleQueueUpdateMessage` | ✓ `TestQueueSDK_UpdateMessageExtendsInvisibility`, `TestQueueSDK_UpdateMessageMakesAMessageVisibleAgain` | ✓ `TestQueueMessageUpdateCLI` | n/a | Validates the pop receipt, really extends the invisibility, replaces the content and issues a superseding `x-ms-popreceipt` with `x-ms-time-next-visible`. |
| MessageId_Delete | `DELETE /{queue}/messages/{messageid}?popreceipt=…` | ✓ `handleQueueDeleteMessage` | ✓ `TestStorageSDK_QueueLifecycleAndPagedLists` | ✓ `TestQueueMessageUpdateCLI` | n/a | A pop receipt that does not match answers `PopReceiptMismatch` (400) rather than reporting a deletion that did not happen. |

## Tables

| Operation | Verb + path | sim handler | sdk-test | raw-wire test | paged-shape verified | notes |
|---|---|---|---|---|---|---|
| CreateTable | `POST /Tables` | ✓ `handleTableCreate` | ✓ `TestStorageSDK_TableLifecycleAndPagedLists` | ✓ `TestTablesDataPlane` | n/a | |
| GetTable | `GET /Tables('{table}')` | ✓ `handleTableGet` | n/a | ✓ `TestTablesDataPlane` | n/a | The official aztables SDK exposes no Get Table operation (`az storage table exists` also queries via ListTables), so the raw-wire test is the canonical client surface for this path. |
| DeleteTable | `DELETE /Tables('{table}')` | ✓ `handleTableDelete` | ✓ `TestStorageSDK_TableLifecycleAndPagedLists` | ✓ `TestTablesDataPlane` | n/a | Cascades entities. |
| ListTables | `GET /Tables` | ✓ `handleTablesList` | ✓ `TestStorageSDK_TableLifecycleAndPagedLists` | ✓ `TestTableListAndUpsertEntityCLI` | ✓ SDK pager | |
| AddEntity | `POST /{table}` | ✓ `handleEntityInsert` | ✓ `TestStorageSDK_TableLifecycleAndPagedLists` | ✓ `TestTablesDataPlane` | n/a | |
| GetEntity | `GET /{table}(PartitionKey='...',RowKey='...')` | ✓ `handleEntityGet` | ✓ `TestStorageSDK_TableLifecycleAndPagedLists` | ✓ `TestTablesDataPlane` | n/a | |
| UpsertEntity | `PUT/PATCH/MERGE /{table}(PartitionKey='...',RowKey='...')` | ✓ `handleEntityUpsert` | ✓ `TestStorageSDK_TableUpsertEntity` | ✓ `TestTableListAndUpsertEntityCLI` | n/a | Replace mode swaps the entity wholesale; merge mode folds new properties in. |
| DeleteEntity | `DELETE /{table}(PartitionKey='...',RowKey='...')` | ✓ `handleEntityDelete` | ✓ `TestStorageSDK_TableLifecycleAndPagedLists` | ✓ `TestTablesDataPlane` | n/a | |
| QueryEntities | `GET /{table}` / `GET /{table}()` | ✓ `handleEntityQuery` | ✓ `TestStorageSDK_TableLifecycleAndPagedLists` | ✓ `TestTablesDataPlane` | ✓ SDK pager | SDK uses the `/{table}()` form. |

## Follow-up audit note

This table closes the BUG-1183 coverage gap for the current Storage data-plane slice. Every implemented operation is exercised through a canonical client: the official azure-sdk-for-go storage clients in `simulator-azure/sdk-tests/` and either a raw-wire probe or an `az storage` / `az rest` invocation in `simulator-azure/cli-tests/`. Full continuation-token pagination remains a broader Storage fidelity audit item; canonical SDK pager call paths are verified for every supported List operation.
