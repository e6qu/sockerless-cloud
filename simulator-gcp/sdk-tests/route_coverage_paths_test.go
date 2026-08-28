package gcp_sdk_test

import (
	"io"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Literal wire-path index for the simulator-testing-contract hook.
//
// The routes below are driven by the Google Cloud Go SDK round-trip tests in
// this package, but the SDK builds request URLs internally, so their literal
// wire paths never appear in test source and the hook — which greps changed
// test files for each route's literal path — cannot see them. This block is
// where they are written down.
//
// A comment satisfies a grep whether or not the route exists, so the block is
// not taken on trust: TestRouteCoveragePathsAreServed reads these lines back
// out of this file and sends each one to the running simulator, failing on any
// that no handler answers. That check proves the routes are real; it does not
// prove any particular SDK call drives them, which is the sibling tests' job.
//
//   DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}
//   DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}
//   DELETE /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}
//   DELETE /bigquery/v2/projects/{project}/jobs/{job}/delete
//   DELETE /dns/v1/projects/{project}/policies/{policy}
//   DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}
//   DELETE /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}
//   DELETE /v1/projects/{project}/databases/{database}
//   DELETE /v1/projects/{project}/databases/{database}/backupSchedules/{bs}
//   DELETE /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}
//   DELETE /v1/projects/{project}/databases/{database}/operations/{operation}
//   DELETE /v1/projects/{project}/databases/{database}/userCreds/{uc}
//   DELETE /v1/projects/{project}/githubEnterpriseConfigs/{config}
//   DELETE /v1/projects/{project}/locations/{location}/backups/{backup}
//   DELETE /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}
//   DELETE /v1/projects/{project}/locations/{location}/enrollments/{enrollment}
//   DELETE /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}
//   DELETE /v1/projects/{project}/locations/{location}/googleApiSources/{source}
//   DELETE /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}
//   DELETE /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}
//   DELETE /v1/projects/{project}/locations/{location}/messageBuses/{bus}
//   DELETE /v1/projects/{project}/locations/{location}/operations/{operation}
//   DELETE /v1/projects/{project}/locations/{location}/pipelines/{pipeline}
//   DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}
//   DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}
//   DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}
//   DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}
//   DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}
//   DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}
//   DELETE /v1/projects/{project}/locations/{location}/workerPools/{pool}
//   DELETE /v1/projects/{project}/schemas/{schemaVerb}
//   GET /bigquery/v2/projects
//   GET /bigquery/v2/projects/{project}/datasets/{dataset}/models
//   GET /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}
//   GET /bigquery/v2/projects/{project}/datasets/{dataset}/routines
//   GET /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}
//   GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies
//   GET /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}
//   GET /bigquery/v2/projects/{project}/serviceAccount
//   GET /dns/v1/projects/{project}
//   GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys
//   GET /dns/v1/projects/{project}/managedZones/{zone}/dnsKeys/{dnsKeyId}
//   GET /dns/v1/projects/{project}/managedZones/{zone}/operations
//   GET /dns/v1/projects/{project}/managedZones/{zone}/operations/{operation}
//   GET /dns/v1/projects/{project}/policies
//   GET /dns/v1/projects/{project}/policies/{policy}
//   GET /dns/v1/projects/{project}/responsePolicies
//   GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}
//   GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules
//   GET /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}
//   GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{fileAction}
//   GET /v1/folders/{folder}/autokeyConfig
//   GET /v1/folders/{folder}/kajPolicyConfig
//   GET /v1/operations/{operation...}
//   GET /v1/organizations/{organization}/kajPolicyConfig
//   GET /v1/projects/{project}/autokeyConfig
//   GET /v1/projects/{project}/databases
//   GET /v1/projects/{project}/databases/{database}
//   GET /v1/projects/{project}/databases/{database}/backupSchedules
//   GET /v1/projects/{project}/databases/{database}/backupSchedules/{bs}
//   GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields
//   GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}
//   GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes
//   GET /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes/{index}
//   GET /v1/projects/{project}/databases/{database}/operations
//   GET /v1/projects/{project}/databases/{database}/operations/{operation}
//   GET /v1/projects/{project}/databases/{database}/userCreds
//   GET /v1/projects/{project}/databases/{database}/userCreds/{uc}
//   GET /v1/projects/{project}/githubEnterpriseConfigs
//   GET /v1/projects/{project}/githubEnterpriseConfigs/{config}
//   GET /v1/projects/{project}/kajPolicyConfig
//   GET /v1/projects/{project}/locations
//   GET /v1/projects/{project}/locations/{location}
//   GET /v1/projects/{project}/locations/{location}/{locationGetAction}
//   GET /v1/projects/{project}/locations/{location}/backups
//   GET /v1/projects/{project}/locations/{location}/backups/{backup}
//   GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs
//   GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}
//   GET /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}/repos
//   GET /v1/projects/{project}/locations/{location}/builds
//   GET /v1/projects/{project}/locations/{location}/builds/{id}
//   GET /v1/projects/{project}/locations/{location}/defaultServiceAccount
//   GET /v1/projects/{project}/locations/{location}/ekmConfig
//   GET /v1/projects/{project}/locations/{location}/ekmConnections
//   GET /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnectionAction}
//   GET /v1/projects/{project}/locations/{location}/enrollments
//   GET /v1/projects/{project}/locations/{location}/enrollments/{enrollment}
//   GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs
//   GET /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}
//   GET /v1/projects/{project}/locations/{location}/googleApiSources
//   GET /v1/projects/{project}/locations/{location}/googleApiSources/{source}
//   GET /v1/projects/{project}/locations/{location}/googleChannelConfig
//   GET /v1/projects/{project}/locations/{location}/instances/{id}/authString
//   GET /v1/projects/{project}/locations/{location}/keyHandles
//   GET /v1/projects/{project}/locations/{location}/keyHandles/{keyHandle}
//   GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/cryptoKeyVersions/{cryptoKeyVersion}/publicKey
//   GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs
//   GET /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs/{importJobAction}
//   GET /v1/projects/{project}/locations/{location}/messageBuses
//   GET /v1/projects/{project}/locations/{location}/messageBuses/{bus}
//   GET /v1/projects/{project}/locations/{location}/operations
//   GET /v1/projects/{project}/locations/{location}/pipelines
//   GET /v1/projects/{project}/locations/{location}/pipelines/{pipeline}
//   GET /v1/projects/{project}/locations/{location}/projectConfig
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages/{image}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts/{artifact}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages/{npmPackage}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts
//   POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload
//   POST /v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages/{pythonPackage}
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules
//   GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}
//   GET /v1/projects/{project}/locations/{location}/retiredResources
//   GET /v1/projects/{project}/locations/{location}/retiredResources/{retiredResource}
//   GET /v1/projects/{project}/locations/{location}/vpcscConfig
//   GET /v1/projects/{project}/locations/{location}/workerPools
//   GET /v1/projects/{project}/locations/{location}/workerPools/{pool}
//   GET /v1/projects/{project}/projectSettings
//   GET /v1/projects/{project}/schemas
//   GET /v1/projects/{project}/schemas/{schemaVerb}
//   GET /v1/projects/{project}/topics/{topic}/snapshots
//   GET /v1/projects/{project}/topics/{topic}/subscriptions
//   PATCH /bigquery/v2/projects/{project}/datasets/{dataset}/models/{model}
//   PATCH /dns/v1/projects/{project}/managedZones/{zone}
//   PATCH /dns/v1/projects/{project}/policies/{policy}
//   PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}
//   PATCH /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}
//   PATCH /v1/folders/{folder}/autokeyConfig
//   PATCH /v1/folders/{folder}/kajPolicyConfig
//   PATCH /v1/organizations/{organization}/kajPolicyConfig
//   PATCH /v1/projects/{project}/autokeyConfig
//   PATCH /v1/projects/{project}/databases/{database}
//   PATCH /v1/projects/{project}/databases/{database}/backupSchedules/{bs}
//   PATCH /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/fields/{field}
//   PATCH /v1/projects/{project}/githubEnterpriseConfigs/{config}
//   PATCH /v1/projects/{project}/kajPolicyConfig
//   PATCH /v1/projects/{project}/locations/{location}/bitbucketServerConfigs/{config}
//   PATCH /v1/projects/{project}/locations/{location}/ekmConfig
//   PATCH /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnection}
//   PATCH /v1/projects/{project}/locations/{location}/enrollments/{enrollment}
//   PATCH /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs/{config}
//   PATCH /v1/projects/{project}/locations/{location}/googleApiSources/{source}
//   PATCH /v1/projects/{project}/locations/{location}/googleChannelConfig
//   PATCH /v1/projects/{project}/locations/{location}/messageBuses/{bus}
//   PATCH /v1/projects/{project}/locations/{location}/pipelines/{pipeline}
//   PATCH /v1/projects/{project}/locations/{location}/projectConfig
//   PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}
//   PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}
//   PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}
//   PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}
//   PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}
//   PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}
//   PATCH /v1/projects/{project}/locations/{location}/vpcscConfig
//   PATCH /v1/projects/{project}/locations/{location}/workerPools/{pool}
//   PATCH /v1/projects/{project}/projectSettings
//   PATCH /v1/projects/{project}/snapshots/{snap}
//   POST /bigquery/v2/projects/{project}/datasets/{dataset}/routines
//   POST /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routineVerb}
//   POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies:batchDelete
//   POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies
//   POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policyVerb}
//   POST /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{tableVerb}
//   POST /bigquery/v2/projects/{project}/datasets/{datasetVerb}
//   POST /bigquery/v2/projects/{project}/jobs/{job}/cancel
//   POST /dns/v1/projects/{project}/policies
//   POST /dns/v1/projects/{project}/responsePolicies
//   POST /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules
//   POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/
//   POST /v1/projects/{project}/databases
//   POST /v1/projects/{project}/databases/{database}/backupSchedules
//   POST /v1/projects/{project}/databases/{database}/collectionGroups/{cg}/indexes
//   POST /v1/projects/{project}/databases/{database}/operations/{opAction}
//   POST /v1/projects/{project}/databases/{database}/userCreds
//   POST /v1/projects/{project}/databases/{database}/userCreds/{ucAction}
//   POST /v1/projects/{project}/databases/{dbAction}
//   POST /v1/projects/{project}/githubEnterpriseConfigs
//   POST /v1/projects/{project}/locations/{location}/bitbucketServerConfigs
//   POST /v1/projects/{project}/locations/{location}/channelConnections/{connectionAction}
//   POST /v1/projects/{project}/locations/{location}/channels/{channelAction}
//   POST /v1/projects/{project}/locations/{location}/ekmConfig/{ekmConfigAction}
//   POST /v1/projects/{project}/locations/{location}/ekmConnections
//   POST /v1/projects/{project}/locations/{location}/ekmConnections/{ekmConnectionAction}
//   POST /v1/projects/{project}/locations/{location}/enrollments
//   POST /v1/projects/{project}/locations/{location}/enrollments/{enrollmentAction}
//   POST /v1/projects/{project}/locations/{location}/githubEnterpriseConfigs
//   POST /v1/projects/{project}/locations/{location}/googleApiSources
//   POST /v1/projects/{project}/locations/{location}/googleApiSources/{sourceAction}
//   POST /v1/projects/{project}/locations/{location}/keyHandles
//   POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/{cryptoKeyVersionsCollectionAction}
//   POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs
//   POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/importJobs/{importJobAction}
//   POST /v1/projects/{project}/locations/{location}/keyRings/{keyRingAction}
//   POST /v1/projects/{project}/locations/{location}/messageBuses
//   POST /v1/projects/{project}/locations/{location}/messageBuses/{busAction}
//   POST /v1/projects/{project}/locations/{location}/pipelines
//   POST /v1/projects/{project}/locations/{location}/pipelines/{pipelineAction}
//   POST /v1/projects/{project}/locations/{location}/repositories/{repo}/
//   POST /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments
//   POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags
//   POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions:batchDelete
//   POST /v1/projects/{project}/locations/{location}/repositories/{repo}/rules
//   POST /v1/projects/{project}/locations/{location}/triggers/{triggerAction}
//   POST /v1/projects/{project}/locations/{location}/workerPools
//   POST /v1/projects/{project}/locations/{locationAction}
//   POST /v1/projects/{project}/schemas:validate
//   POST /v1/projects/{project}/schemas:validateMessage
//   POST /v1/projects/{project}/schemas
//   POST /v1/projects/{project}/schemas/{schemaVerb}
//   POST /v1/projects/{project}/snapshots/{snapVerb}
//   PUT /bigquery/v2/projects/{project}/datasets/{dataset}/routines/{routine}
//   PUT /bigquery/v2/projects/{project}/datasets/{dataset}/tables/{table}/rowAccessPolicies/{policy}
//   PUT /dns/v1/projects/{project}/managedZones/{zone}
//   PUT /dns/v1/projects/{project}/policies/{policy}
//   PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}
//   PUT /dns/v1/projects/{project}/responsePolicies/{responsePolicy}/rules/{rule}
//   PUT /v1/projects/{project}/serviceAccounts/{email}

// Cloud Run v1 (Knative) + v2 ratchet routes — each is mounted in
// simulator-gcp/cloudrun.go or cloudrunservices.go and exercised by a
// genuine SDK round-trip in this package (cloudrun_knative_resources_test.go,
// cloudrun_v2_revisions_iam_test.go). The Google clients build the wire URLs
// internally, so the literal paths are recorded here for the testing-contract
// hook's grep:
//
//   GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations
//   GET /apis/serving.knative.dev/v1/namespaces/{namespace}/configurations/{name}
//   GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions
//   GET /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}
//   DELETE /apis/serving.knative.dev/v1/namespaces/{namespace}/revisions/{name}
//   GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes
//   GET /apis/serving.knative.dev/v1/namespaces/{namespace}/routes/{name}
//   POST /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings
//   GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings
//   GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}
//   DELETE /apis/domains.cloudrun.com/v1/namespaces/{namespace}/domainmappings/{name}
//   GET /apis/domains.cloudrun.com/v1/namespaces/{namespace}/authorizeddomains
//   GET /v1/projects/{project}/authorizeddomains
//   GET /v1/projects/{project}/locations/{location}/authorizeddomains
//   POST /v1/projects/{project}/locations/{namespace}/services
//   GET /v1/projects/{project}/locations/{namespace}/services
//   GET /v1/projects/{project}/locations/{namespace}/services/{name}
//   PUT /v1/projects/{project}/locations/{namespace}/services/{name}
//   DELETE /v1/projects/{project}/locations/{namespace}/services/{name}
//   POST /v1/projects/{project}/locations/{namespace}/services/{nameAction}
//   GET /v1/projects/{project}/locations/{namespace}/configurations
//   GET /v1/projects/{project}/locations/{namespace}/configurations/{name}
//   GET /v1/projects/{project}/locations/{namespace}/revisions
//   GET /v1/projects/{project}/locations/{namespace}/revisions/{name}
//   DELETE /v1/projects/{project}/locations/{namespace}/revisions/{name}
//   GET /v1/projects/{project}/locations/{namespace}/routes
//   GET /v1/projects/{project}/locations/{namespace}/routes/{name}
//   POST /v1/projects/{project}/locations/{namespace}/domainmappings
//   GET /v1/projects/{project}/locations/{namespace}/domainmappings
//   GET /v1/projects/{project}/locations/{namespace}/domainmappings/{name}
//   DELETE /v1/projects/{project}/locations/{namespace}/domainmappings/{name}
//   GET /v2/projects/{project}/locations/{location}/services/{service}/revisions
//   GET /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}
//   DELETE /v2/projects/{project}/locations/{location}/services/{service}/revisions/{revision}
//   POST /v2/projects/{project}/locations/{location}/services/{serviceAction}
//   DELETE /v2/projects/{project}/locations/{location}/operations/{operation}
//   POST /v2/projects/{project}/locations/{location}/operations/{opAction}
//
// Cloud Dataflow v1b3 — driven by the TestDataflow_* round-trips in
// spanner_dataflow_bigtable_test.go. Each job/template/snapshot family is
// mounted under both the global (projects/{project}/...) and regional
// (projects/{project}/locations/{location}/...) path forms.
//   POST /v1b3/projects/{project}/jobs
//   GET /v1b3/projects/{project}/jobs
//   GET /v1b3/projects/{project}/jobs:aggregated
//   GET /v1b3/projects/{project}/jobs/{jobAction}
//   PUT /v1b3/projects/{project}/jobs/{job}
//   POST /v1b3/projects/{project}/jobs/{jobAction}
//   GET /v1b3/projects/{project}/jobs/{job}/metrics
//   GET /v1b3/projects/{project}/jobs/{job}/messages
//   POST /v1b3/projects/{project}/jobs/{job}/debug/getConfig
//   POST /v1b3/projects/{project}/jobs/{job}/debug/sendCapture
//   POST /v1b3/projects/{project}/jobs/{job}/workItems:lease
//   POST /v1b3/projects/{project}/jobs/{job}/workItems:reportStatus
//   POST /v1b3/projects/{project}/locations/{location}/jobs
//   GET /v1b3/projects/{project}/locations/{location}/jobs
//   GET /v1b3/projects/{project}/locations/{location}/jobs/{job}
//   PUT /v1b3/projects/{project}/locations/{location}/jobs/{job}
//   POST /v1b3/projects/{project}/locations/{location}/jobs/{jobAction}
//   GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/metrics
//   GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/messages
//   GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/executionDetails
//   GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/snapshots
//   GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/stages/{stage}/executionDetails
//   POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/getConfig
//   POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/sendCapture
//   POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/getWorkerStacktraces
//   POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/workItems:lease
//   POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/workItems:reportStatus
//   POST /v1b3/projects/{project}/templates
//   GET /v1b3/projects/{project}/templates:get
//   POST /v1b3/projects/{project}/templates:launch
//   POST /v1b3/projects/{project}/locations/{location}/templates
//   GET /v1b3/projects/{project}/locations/{location}/templates:get
//   POST /v1b3/projects/{project}/locations/{location}/templates:launch
//   POST /v1b3/projects/{project}/locations/{location}/flexTemplates:launch
//   GET /v1b3/projects/{project}/snapshots
//   DELETE /v1b3/projects/{project}/snapshots
//   GET /v1b3/projects/{project}/snapshots/{snapshot}
//   GET /v1b3/projects/{project}/locations/{location}/snapshots
//   GET /v1b3/projects/{project}/locations/{location}/snapshots/{snapshot}
//   DELETE /v1b3/projects/{project}/locations/{location}/snapshots/{snapshot}
//   POST /v1b3/projects/{project}/WorkerMessages
//   POST /v1b3/projects/{project}/locations/{location}/WorkerMessages
//
// Bigtable Admin (bigtableadmin/v2) — exercised by TestBigtable_AdminSurfaceSDK
// and TestBigtable_InstanceClusterTableSDK. The SDK builds these wire paths
// internally; each is a real route mounted in simulator-gcp/bigtable.go and
// driven by a genuine SDK round-trip in this package.
//   POST /v2/projects/{project}/instances/{instanceAction}
//   PUT /v2/projects/{project}/instances/{instance}
//   GET /v2/operations/projects/{project}/operations
//   GET /v2/operations/{operation...}
//   PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}
//   GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/hotTablets
//   GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayer
//   GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/memoryLayers
//   POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups
//   GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups
//   POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/{backupsColl}
//   POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backupAction}
//   GET /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}
//   PATCH /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}
//   DELETE /v2/projects/{project}/instances/{instance}/clusters/{cluster}/backups/{backup}
//   POST /v2/projects/{project}/instances/{instance}/appProfiles
//   GET /v2/projects/{project}/instances/{instance}/appProfiles
//   GET /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}
//   PATCH /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}
//   DELETE /v2/projects/{project}/instances/{instance}/appProfiles/{appProfile}
//   POST /v2/projects/{project}/instances/{instance}/{tablesColl}
//   GET /v2/projects/{project}/instances/{instance}/tables/{table}
//   PATCH /v2/projects/{project}/instances/{instance}/tables/{table}
//   POST /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews
//   GET /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews
//   POST /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authViewAction}
//   GET /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}
//   PATCH /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}
//   DELETE /v2/projects/{project}/instances/{instance}/tables/{table}/authorizedViews/{authView}
//   POST /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles
//   GET /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles
//   POST /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundleAction}
//   GET /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}
//   PATCH /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}
//   DELETE /v2/projects/{project}/instances/{instance}/tables/{table}/schemaBundles/{schemaBundle}
//   POST /v2/projects/{project}/instances/{instance}/logicalViews
//   GET /v2/projects/{project}/instances/{instance}/logicalViews
//   POST /v2/projects/{project}/instances/{instance}/logicalViews/{logicalViewAction}
//   GET /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}
//   PATCH /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}
//   DELETE /v2/projects/{project}/instances/{instance}/logicalViews/{logicalView}
//   POST /v2/projects/{project}/instances/{instance}/materializedViews
//   GET /v2/projects/{project}/instances/{instance}/materializedViews
//   POST /v2/projects/{project}/instances/{instance}/materializedViews/{matViewAction}
//   GET /v2/projects/{project}/instances/{instance}/materializedViews/{matView}
//   PATCH /v2/projects/{project}/instances/{instance}/materializedViews/{matView}
//   DELETE /v2/projects/{project}/instances/{instance}/materializedViews/{matView}
//
// Cloud Logging v2 admin — driven by the TestLogging_* round-trips in
// logging_test.go and logging_admin_test.go. The sinks/exclusions/buckets/
// views/links families mount under each parent scope via concatenated route
// patterns (the SDK builds the wire URL internally). The three literal-mounted
// top-level routes below are recorded here so the simulator-testing-contract
// hook sees their coverage.
//   POST /v2/entries:copy
//   POST /v2/entries:tail
//   GET /v2/monitoredResourceDescriptors
//
// GCP ratchet round 2 (BUG-2220) — CRM v3 / Spanner / IAM Credentials / Cloud
// Functions / API Gateway / VPC Access routes exercised by the SDK round-trips
// in this package (the SDK builds request URLs internally):
//   DELETE /v1/operations/{operation}
//   DELETE /v3/folders/{folder}
//   DELETE /v3/liens/{lien}
//   DELETE /v3/projects/{project}
//   DELETE /v3/tagBindings/{binding...}
//   DELETE /v3/tagKeys/{key}
//   DELETE /v3/tagValues/{val}
//   DELETE /v3/tagValues/{val}/tagHolds/{hold}
//   GET /spanner/v1/projects/{project}/instanceConfigOperations
//   GET /spanner/v1/scans
//   GET /v1/locations/{location}/workforcePools/{pool}/allowedLocations
//   GET /v1/projects/{project}/locations/{location}/workloadIdentityPools/{pool}/allowedLocations
//   GET /v1/projects/{project}/serviceAccounts/{email}/allowedLocations
//   GET /v2/projects/{project}/locations/{location}/runtimes
//   GET /v3/effectiveTags
//   GET /v3/folders
//   GET /v3/folders:search
//   GET /v3/folders/{folder}
//   GET /v3/folders/{folder}/capabilities/{capability}
//   GET /v3/liens
//   GET /v3/liens/{lien}
//   GET /v3/locations/{location}/effectiveTagBindingCollections/{collection}
//   GET /v3/locations/{location}/tagBindingCollections/{collection}
//   GET /v3/operations/{operation}
//   GET /v3/organizations:search
//   GET /v3/organizations/{org}
//   GET /v3/projects
//   GET /v3/projects:search
//   GET /v3/projects/{project}
//   GET /v3/tagBindings
//   GET /v3/tagKeys
//   GET /v3/tagKeys/{key}
//   GET /v3/tagKeys/namespaced
//   GET /v3/tagValues
//   GET /v3/tagValues/{val}
//   GET /v3/tagValues/{val}/tagHolds
//   GET /v3/tagValues/namespaced
//   PATCH /v1/projects/{project}/locations/{location}/connectors/{name}
//   PATCH /v1/projects/{project}/locations/{location}/gateways/{gw}
//   PATCH /v1/projects/{project}/locations/global/apis/{api}
//   PATCH /v1/projects/{project}/locations/global/apis/{api}/configs/{cfg}
//   PATCH /v3/folders/{folder}
//   PATCH /v3/folders/{folder}/capabilities/{capability}
//   PATCH /v3/locations/{location}/tagBindingCollections/{collection}
//   PATCH /v3/projects/{project}
//   PATCH /v3/tagKeys/{key}
//   PATCH /v3/tagValues/{val}
//   POST /v2/projects/{project}/locations/{location}/functions/{functionAction}
//   POST /v3/folders
//   POST /v3/folders/{folderAction}
//   POST /v3/liens
//   POST /v3/organizations/{orgAction}
//   POST /v3/projects
//   POST /v3/projects/{projectAction}
//   POST /v3/tagBindings
//   POST /v3/tagKeys
//   POST /v3/tagKeys/{keyAction}
//   POST /v3/tagValues
//   POST /v3/tagValues/{val}/tagHolds
//   POST /v3/tagValues/{valAction}

// Cloud Run v2 ratchet: worker pools, instances, job update, execution
// delete, and tasks. These routes are driven by the run/apiv2 clients in
// cloudrun_more_test.go and by the Discovery REST clients
// (google.golang.org/api/run/v2 and /run/v1) in
// cloudrun_v2_workerpools_instances_rest_test.go; the literal wire paths are
// recorded here so the simulator-testing-contract hook can see the coverage.
//
//   POST /v2/projects/{project}/locations/{location}/workerPools
//   GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}
//   GET /v2/projects/{project}/locations/{location}/workerPools
//   PATCH /v2/projects/{project}/locations/{location}/workerPools/{workerPool}
//   DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}
//   GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}
//   GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions
//   DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}
//   POST /v2/projects/{project}/locations/{location}/workerPools/{workerPoolAction}
//   GET /v1/projects/{project}/locations/{location}/workerpools/{workerPool}
//   POST /v1/projects/{project}/locations/{location}/workerpools/{workerPoolAction}
//   POST /v2/projects/{project}/locations/{location}/instances
//   GET /v2/projects/{project}/locations/{location}/instances/{instance}
//   GET /v2/projects/{project}/locations/{location}/instances
//   PATCH /v2/projects/{project}/locations/{location}/instances/{instance}
//   DELETE /v2/projects/{project}/locations/{location}/instances/{instance}
//   POST /v2/projects/{project}/locations/{location}/instances/{instanceAction}
//   PATCH /v2/projects/{project}/locations/{location}/jobs/{job}
//   DELETE /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}
//   GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks/{task}
//   GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks

// Cloud Billing account collection, project links and service catalog,
// driven by google.golang.org/api/cloudbilling/v1 in cloudbilling_test.go
// and by gcloud billing in cli-tests/cloudbilling_cli_test.go; the literal
// wire paths are recorded here so the simulator-testing-contract hook can
// see the coverage.
//
//   GET /v1/billingAccounts
//   POST /v1/billingAccounts
//   GET /v1/billingAccounts/{accountAction}
//   POST /v1/billingAccounts/{accountAction}
//   PATCH /v1/billingAccounts/{account}
//   GET /v1/billingAccounts/{account}/subAccounts
//   POST /v1/billingAccounts/{account}/subAccounts
//   GET /v1/billingAccounts/{account}/projects
//   GET /v1/organizations/{organization}/billingAccounts
//   POST /v1/organizations/{organization}/billingAccounts
//   GET /v1/organizations/{organization}/billingAccounts/{accountAction}
//   PUT /v1/projects/{project}/billingInfo
//   GET /v1/services
//   GET /v1/services/{service}/skus

// Cloud Resource Manager project lifecycle (v1 + v3) and the Cloud Billing
// project billing-info read. These routes are driven by
// cloud.google.com/go/resourcemanager/apiv3 (create/list/search/get/delete/
// undelete with LRO polling), google.golang.org/api/cloudresourcemanager/v1
// (the wire gcloud `projects` and terraform's google_project speak), and
// google.golang.org/api/cloudbilling/v1 in resourcemanager_projects_test.go
// and cloudresourcemanager_v3_test.go; the literal wire paths are recorded
// here so the simulator-testing-contract hook can see the coverage.
//
//   POST /v1/projects
//   GET /v1/projects
//   GET /v1/projects/{project}
//   PUT /v1/projects/{project}
//   DELETE /v1/projects/{project}
//   POST /v1/projects/{projectAction}
//   GET /v1/operations/{operation}
//   GET /v1/projects/{project}/billingInfo

// Cloud Run Admin v1 (Knative) Jobs family. These routes render the v2-owned
// job / execution / task records in the Knative shape and are driven by the
// canonical google.golang.org/api/run/v1 client in cloudrun_v1_jobs_test.go
// (and, for the jobs IAM read, by `gcloud run jobs get-iam-policy` in
// cli-tests/run_jobs_iam_test.go). The literal wire paths are recorded here so
// the simulator-testing-contract hook can see the coverage.
//
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks/{name}
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks
//   GET /v1/projects/{project}/locations/{location}/jobs/{jobAction}
//   POST /v1/projects/{project}/locations/{location}/operations/{opAction}

// Cloud Run Admin v1 (Knative) instances and worker pools. These routes render
// the v2-owned instance and worker-pool records in the Knative shape and are
// driven by google.golang.org/api/run/v1 in
// cloudrun_v1_instances_workerpools_test.go. `gcloud run worker-pools` and
// `gcloud alpha run instances` reach these collections only at the regional
// Cloud Run host, which no endpoint coordinate can point at a loopback
// simulator (see cli-tests/run_workerpools_v2_test.go), so the SDK is the
// client surface here.
//
//   POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances
//   PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}
//   DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}
//   POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{nameAction}
//   POST /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools
//   PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}
//   DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}

// Cloud Run Admin v1 (Knative) jobs, and the v2 jobs colon-verb fan-in that
// dispatches RunJob and the POST-side IAM verbs. The Knative jobs collection
// writes and reads the same records the v2 collection owns and drives the same
// container execution; both are driven by google.golang.org/api/run/v1 and raw
// v2 wire calls in cloudrun_v1_jobs_lifecycle_test.go, including a real image
// whose non-zero exit reaches status.failedCount and a cancel that stops the
// running container. `gcloud run jobs create/execute/describe` reach these
// collections only at the regional Cloud Run host, which no endpoint
// coordinate can point at a loopback simulator; only the global-path IAM
// commands are CLI-reachable (cli-tests/run_jobs_iam_test.go).
//
//   POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}
//   GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs
//   PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}
//   DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}
//   POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{nameAction}
//   DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}
//   POST /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{nameAction}
//   POST /v2/projects/{project}/locations/{location}/jobs/{jobAction}

// routeCoverageLine matches one entry of the block above: the method and the
// wire path, as the hook reads them.
var routeCoverageLine = regexp.MustCompile(`^//\s{3}([A-Z]+) (/\S*)$`)

// routeCoveragePathParam matches a path template's placeholder segment.
var routeCoveragePathParam = regexp.MustCompile(`\{[^}]*\}`)

// TestRouteCoveragePathsAreServed holds the block above to its claim. The list
// exists so the simulator-testing-contract hook can see routes whose literal
// wire paths never appear in SDK test source (the SDK builds request URLs
// internally) — but a comment satisfies a grep whether or not the route exists,
// so the list would otherwise be able to name a route nothing serves and still
// green the gate for it.
//
// Every entry is therefore rendered into a concrete URI and sent to the running
// simulator. Go's own plain-text mux miss is the failure: it means no handler is
// mounted at that method and path. A service-level refusal (a JSON 404, a 400,
// a 403) means a handler answered, which is all this list can honestly claim —
// that the route is real. What the sibling tests then assert about it is their
// business, not this file's.
func TestRouteCoveragePathsAreServed(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "locate this file to read the route list out of it")
	source, err := os.ReadFile(thisFile)
	require.NoError(t, err)

	var routes [][2]string
	for _, line := range strings.Split(string(source), "\n") {
		if m := routeCoverageLine.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			routes = append(routes, [2]string{m[1], m[2]})
		}
	}
	require.Greater(t, len(routes), 400,
		"the route list must be readable — a parse that finds nothing would pass vacuously")

	seen := map[string]bool{}
	for _, route := range routes {
		method, template := route[0], route[1]
		key := method + " " + template
		if seen[key] {
			t.Errorf("%s is listed twice", key)
			continue
		}
		seen[key] = true

		// A route mounted from a concatenated pattern (".../{repo}/" + kind +
		// ":create") is keyed by the hook on its literal prefix, so the entry is
		// a path prefix rather than a whole path and nothing answers it. Such an
		// entry earns its place by prefixing a complete entry in this same list,
		// which is probed on its own line.
		if strings.HasSuffix(template, "/") {
			covered := false
			for _, other := range routes {
				if other[0] == method && other[1] != template && strings.HasPrefix(other[1], template) {
					covered = true
					break
				}
			}
			if !covered {
				t.Errorf("%s is a path prefix that no complete listed route extends", key)
			}
			continue
		}

		// A placeholder stands for any single segment, so any non-empty value
		// routes the same way. `probe` also keeps a colon-verb suffix
		// (`{nameAction}` → `probe`) from accidentally naming a real verb.
		uri := routeCoveragePathParam.ReplaceAllString(template, "probe")
		req, err := http.NewRequestWithContext(ctx, method, baseURL+uri, http.NoBody)
		require.NoError(t, err)
		resp, err := http.DefaultClient.Do(req)
		require.NoErrorf(t, err, "%s", key)
		raw, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.NoError(t, resp.Body.Close())
		body := string(raw)

		if strings.Contains(body, "404 page not found") || strings.Contains(body, "Method Not Allowed") {
			t.Errorf("%s is listed as covered but no handler is mounted for it (%s)",
				key, strings.TrimSpace(body))
		}
	}
}

// The tails served on 2026-08-26: Cloud DNS managed-zone IAM, the IAM v1
// key and catalog methods, Cloud SQL's connect resolve and point-in-time
// restore, Cloud Storage's rapid caches and managed-folder patch, and
// Service Usage's batch read. Driven by served_tails_test.go through the
// generated Go clients, and by the JSON API directly where the generated
// client has no collection yet (rapid caches); the literal wire paths are
// recorded here so the simulator-testing-contract hook can see the
// coverage.
//
//   GET /storage/v1/b/{bucket}/rapidCaches
//   GET /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}
//   POST /storage/v1/b/{bucket}/rapidCaches
//   POST /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}/disable
//   PATCH /storage/v1/b/{bucket}/rapidCaches/{rapidCacheId}
//   PATCH /storage/v1/b/{bucket}/managedFolders/{managedFolder}
//   GET /storage/v1/b/{bucket}/o/{object}/acl
//   GET /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//   POST /storage/v1/b/{bucket}/o/{object}/acl
//   PUT /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//   PATCH /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//   DELETE /storage/v1/b/{bucket}/o/{object}/acl/{entity}
//   POST /storage/v1/b/{bucket}/o/{object}/restore
//   POST /storage/v1/b/{bucket}/o/bulkRestore
//   POST /storage/v1/b/{bucket}/o/{sourceObject}/moveTo/o/{destinationObject}
//   GET /v1/projects/{project}/services:batchGet
//   POST /dns/v1/projects/{project}/managedZones/{zoneAction}
//   POST /sql/v1beta4/projects/{projectAction}
//   POST /v1/iamPolicies:lintPolicy
//   POST /v1/iamPolicies:queryAuditableServices
//   POST /v1/projects/{project}/serviceAccounts/{email}/keys:upload
//   POST /v1/projects/{project}/serviceAccounts/{email}/keys/{keyAction}
//   POST /v1/roles:queryGrantableRoles
