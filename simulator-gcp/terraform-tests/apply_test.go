package gcp_tf_test

import (
	"encoding/base64"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/stretchr/testify/require"
)

// TestTerraformApplyDestroy provisions the full GCP-sim coverage stack
// (compute network + disk + subnet + VPC Access connector + firewall +
// regional address + Cloud NAT + global HTTP load balancer, public + private
// DNS zones, Artifact Registry, Cloud Run v2 Service + Job + Worker Pool,
// Cloud Functions v2,
// Pub/Sub, Cloud Build trigger, Cloud Storage bucket + object, Cloud Logging
// sink/metric, BigQuery dataset/table, Firestore document, Memorystore Redis,
// Cloud SQL instance/database/user, Eventarc trigger, Secret Manager, IAM service
// account) in a single
// terraform apply round-trip and asserts the cross-resource references
// converged.
//
// Slices exercised against the simulator:
//   - compute.googleapis.com (networks + disks + subnetworks + firewalls +
//     addresses + routers + router NATs + healthChecks + backendServices +
//     urlMaps + targetHttpProxies + globalForwardingRules)
//   - vpcaccess.googleapis.com (connectors)
//   - dns.googleapis.com (public + private managedZones + record sets via Changes)
//   - artifactregistry.googleapis.com (Docker repository)
//   - run.googleapis.com v2 (Service + Job + Worker Pool + worker-pool IAM member)
//   - cloudfunctions.googleapis.com v2 (Function)
//   - eventarc.googleapis.com (Trigger)
//   - pubsub.googleapis.com (Topic + Subscription)
//   - cloudbuild.googleapis.com (Trigger)
//   - storage.googleapis.com (bucket + object)
//   - logging.googleapis.com (project sink + metric)
//   - bigquery.googleapis.com (dataset + table)
//   - firestore.googleapis.com (document)
//   - redis.googleapis.com (Memorystore Redis instance)
//   - sqladmin.googleapis.com (Cloud SQL instance + database + user)
//   - secretmanager.googleapis.com (secret + version)
//   - iam.googleapis.com (service account — via iam_beta_custom_endpoint;
//     terraform-provider-google routes the resource through iambeta.NewClient
//     which uses iam_beta_custom_endpoint, NOT iam_custom_endpoint)
//   - spanner.googleapis.com (instance + database)
//   - bigtableadmin.googleapis.com (instance + table)
//   - cloudresourcemanager.googleapis.com v1 (google_project create +
//     operation wait + read + soft-delete on destroy; the org-policy verbs
//     behind google_project_organization_policy /
//     google_folder_organization_policy / google_organization_policy; the
//     liens collection behind google_resource_manager_lien) and the folders
//     collection behind google_folder
//   - cloudbilling.googleapis.com (projects.getBillingInfo, read by the
//     google_project Read path)
func TestTerraformApplyDestroy(t *testing.T) {
	requireTerraformNetworkHost(t)
	cleanTerraformWorkspace(t)
	out, err := runTimed(t, "terraform init", terraformCmd("init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmd("apply", "-auto-approve", "-var", "secret_label_env=dev"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	// Idempotency: a second plan must show no drift. -detailed-exitcode makes
	// terraform exit 2 (non-zero) on any drift, which runTimed surfaces as an
	// error — so a clean plan (exit 0) is the only pass.
	out, err = runTimed(t, "terraform plan", terraformCmd("plan", "-detailed-exitcode", "-var", "secret_label_env=dev"))
	require.NoError(t, err, "terraform plan showed drift after apply (not idempotent):\n%s", out)

	outputs := readOutputs(t)

	diskLink := outputs.must(t, "compute_disk_self_link")
	require.Contains(t, diskLink, "/zones/us-central1-a/disks/tf-test-disk",
		"compute disk self_link must round-trip the zone+name; got %s", diskLink)

	// `instances.insert` answers with a RUNNING zone operation and boots the
	// virtual machine behind it, so the provider reaches these values only by
	// polling zoneOperations through to DONE and re-reading the instance.
	vmLink := outputs.must(t, "compute_instance_self_link")
	require.Contains(t, vmLink, "/zones/us-central1-a/instances/tf-test-vm",
		"compute instance self_link must round-trip the zone+name; got %s", vmLink)
	require.Equal(t, "RUNNING", outputs.must(t, "compute_instance_current_status"),
		"the instance the asynchronous insert booted must be running once its operation is DONE")

	// The IAM Service Account Credentials mint, driven by the provider's
	// google_service_account_access_token data source with a reduced lifetime.
	tokenID := outputs.must(t, "service_account_access_token_id")
	require.Contains(t, tokenID, "projects/-/serviceAccounts/tf-test-runner-sa@",
		"the access-token data source names the impersonated account; got %s", tokenID)
	require.Equal(t, "600s", outputs.must(t, "service_account_access_token_lifetime"),
		"the data source carries the lifetime it requested")
	require.Equal(t, true, outputs.mustValue(t, "service_account_access_token_minted"),
		"a reduced lifetime must still mint a token")

	// What generateAccessToken actually did with that lifetime. `lifetime` is a
	// request member the data source never re-reads, so the state attribute
	// above is the configured literal; the token's own claim span is the API's
	// answer. An implementation that ignored the request and applied the
	// documented one-hour default reports 3600 here.
	require.Equal(t, float64(600),
		outputs.mustNumber(t, "service_account_access_token_lifetime_seconds"),
		"the minted token must expire 600s after it was issued, not at the one-hour default")
	require.Equal(t, "tf-test-runner-sa@test-project.iam.gserviceaccount.com",
		outputs.must(t, "service_account_access_token_subject"),
		"the token must be minted for the impersonated account, not for the caller")

	dnsRecordID := outputs.must(t, "dns_record_set_id")
	require.Contains(t, dnsRecordID, "projects/test-project/managedZones/tf-test-zone/rrsets/www.tf-test.example.com./A",
		"DNS record-set id must include managed zone, FQDN, and type; got %s", dnsRecordID)

	arRepoID := outputs.must(t, "ar_repo_id")
	require.Contains(t, arRepoID, "projects/test-project/locations/us-central1/repositories/tf-ar-docker",
		"AR repo id must be the canonical projects/{p}/locations/{l}/repositories/{id} path; got %s", arRepoID)

	arRemoteRepoID := outputs.must(t, "ar_remote_repo_id")
	require.Contains(t, arRemoteRepoID, "projects/test-project/locations/us-central1/repositories/docker-hub",
		"AR remote repo id must be the canonical projects/{p}/locations/{l}/repositories/{id} path; got %s", arRemoteRepoID)

	crServiceURI := outputs.must(t, "cloud_run_v2_service_uri")
	// Real Cloud Run returns https://<service>-<hash>.<region>.run.app; the
	// sim returns its local invocation URL (http://host:port/v2-services-invoke/...).
	// Both must include the service name so callers can target it.
	require.Contains(t, crServiceURI, "tf-crv2-svc",
		"Cloud Run v2 service URI must reference the service name; got %s", crServiceURI)

	crJobID := outputs.must(t, "cloud_run_v2_job_id")
	require.Contains(t, crJobID, "projects/test-project/locations/us-central1/jobs/tf-crv2-job",
		"Cloud Run v2 job id must round-trip the full resource path; got %s", crJobID)

	crWorkerPoolID := outputs.must(t, "cloud_run_v2_worker_pool_id")
	require.Contains(t, crWorkerPoolID, "projects/test-project/locations/us-central1/workerPools/tf-crv2-worker-pool",
		"Cloud Run v2 worker pool id must round-trip the full resource path; got %s", crWorkerPoolID)

	// A worker pool deploy materializes a revision and reports it ready — the
	// value a caller reads to know which revision is serving.
	crWorkerPoolRevision := outputs.must(t, "cloud_run_v2_worker_pool_latest_ready_revision")
	require.Contains(t, crWorkerPoolRevision,
		"projects/test-project/locations/us-central1/workerPools/tf-crv2-worker-pool/revisions/tf-crv2-worker-pool-",
		"Cloud Run v2 worker pool must report a ready revision under its own resource path; got %s", crWorkerPoolRevision)

	crWorkerPoolIAMID := outputs.must(t, "cloud_run_v2_worker_pool_iam_member_id")
	require.Contains(t, crWorkerPoolIAMID,
		"projects/test-project/locations/us-central1/workerPools/tf-crv2-worker-pool/roles/run.invoker/",
		"Cloud Run v2 worker pool IAM member id must bind the role on the pool resource; got %s", crWorkerPoolIAMID)

	// Worker-pool automatic scaling. scaling_mode / min_instance_count /
	// max_instance_count are members the provider sends on the v2 wire that the
	// pinned Discovery revision does not publish; a simulator that dropped them
	// would re-plan the pool forever (the clean `plan -detailed-exitcode` above
	// is the other half of that proof).
	require.Equal(t, "AUTOMATIC", outputs.must(t, "cloud_run_v2_worker_pool_scaling_mode"))
	require.Equal(t, float64(1), outputs.mustNumber(t, "cloud_run_v2_worker_pool_min_instance_count"))
	require.Equal(t, float64(5), outputs.mustNumber(t, "cloud_run_v2_worker_pool_max_instance_count"))

	// Container probes round-trip through create and read on all three
	// resources that carry them, across all three handler kinds.
	svcStartup := outputs.mustObject(t, "cloud_run_v2_service_startup_probe")
	require.Equal(t, float64(3), svcStartup["initial_delay_seconds"])
	require.Equal(t, float64(2), svcStartup["timeout_seconds"])
	require.Equal(t, float64(10), svcStartup["period_seconds"])
	require.Equal(t, float64(4), svcStartup["failure_threshold"])
	require.Equal(t, float64(8080), mustBlock(t, svcStartup, "tcp_socket")["port"])

	svcLiveness := outputs.mustObject(t, "cloud_run_v2_service_liveness_probe")
	svcLivenessGet := mustBlock(t, svcLiveness, "http_get")
	require.Equal(t, "/healthz", svcLivenessGet["path"])
	require.Equal(t, float64(8080), svcLivenessGet["port"])
	require.Equal(t, []any{map[string]any{"name": "X-Probe", "value": "liveness"}}, svcLivenessGet["http_headers"])

	svcReadiness := outputs.mustObject(t, "cloud_run_v2_service_readiness_probe")
	svcReadinessGRPC := mustBlock(t, svcReadiness, "grpc")
	require.Equal(t, float64(8080), svcReadinessGRPC["port"])
	require.Equal(t, "health.v1.Health", svcReadinessGRPC["service"])

	jobStartup := outputs.mustObject(t, "cloud_run_v2_job_startup_probe")
	require.Equal(t, float64(6), jobStartup["initial_delay_seconds"])
	require.Equal(t, float64(7070), mustBlock(t, jobStartup, "tcp_socket")["port"])

	wpLiveness := outputs.mustObject(t, "cloud_run_v2_worker_pool_liveness_probe")
	require.Equal(t, float64(20), wpLiveness["period_seconds"])
	require.Equal(t, "worker.v1.Health", mustBlock(t, wpLiveness, "grpc")["service"])

	// Environment variables sourced from Secret Manager
	// (Container.env[].valueSource.secretKeyRef) round-trip on every resource.
	for output, wantVersion := range map[string]string{
		"cloud_run_v2_service_secret_env":     "latest",
		"cloud_run_v2_job_secret_env":         "latest",
		"cloud_run_v2_worker_pool_secret_env": "1",
	} {
		env := outputs.mustObject(t, output)
		require.Equal(t, "TF_TEST_SECRET", env["name"], "%s", output)
		ref := mustBlock(t, mustBlock(t, env, "value_source"), "secret_key_ref")
		require.Equal(t, "tf-test-secret", ref["secret"], "%s", output)
		require.Equal(t, wantVersion, ref["version"], "%s", output)
	}

	gcfFunctionID := outputs.must(t, "cloudfunctions2_function_id")
	require.Contains(t, gcfFunctionID, "projects/test-project/locations/us-central1/functions/tf-gcfv2-function",
		"Cloud Functions v2 id must round-trip the full resource path; got %s", gcfFunctionID)

	eventarcID := outputs.must(t, "eventarc_trigger_id")
	require.Contains(t, eventarcID, "projects/test-project/locations/us-central1/triggers/tf-eventarc-trigger",
		"Eventarc trigger id must round-trip the full resource path; got %s", eventarcID)

	pubsubTopicID := outputs.must(t, "pubsub_topic_id")
	require.Equal(t, "projects/test-project/topics/tf-pubsub-topic", pubsubTopicID,
		"Pub/Sub topic id must be the canonical project topic path; got %s", pubsubTopicID)

	pubsubSubscriptionID := outputs.must(t, "pubsub_subscription_id")
	require.Equal(t, "projects/test-project/subscriptions/tf-pubsub-subscription", pubsubSubscriptionID,
		"Pub/Sub subscription id must be the canonical project subscription path; got %s", pubsubSubscriptionID)

	cloudBuildTriggerID := outputs.must(t, "cloudbuild_trigger_id")
	require.Contains(t, cloudBuildTriggerID, "projects/test-project/locations/us-central1/triggers/",
		"Cloud Build trigger id must include the regional trigger path; got %s", cloudBuildTriggerID)

	bucketURL := outputs.must(t, "storage_bucket_url")
	require.True(t, strings.HasPrefix(bucketURL, "gs://tf-test-bucket-"),
		"GCS bucket url must be a gs:// URL; got %s", bucketURL)

	logSinkID := outputs.must(t, "logging_project_sink_id")
	require.Contains(t, logSinkID, "projects/test-project/sinks/tf-log-sink",
		"Logging project sink id must include canonical project sink path; got %s", logSinkID)

	logMetricID := outputs.must(t, "logging_metric_id")
	require.Equal(t, "tf-log-metric", logMetricID,
		"Logging metric id must match terraform-provider-google's metric resource ID shape; got %s", logMetricID)

	secretVersionID := outputs.must(t, "secret_version_id")
	require.Contains(t, secretVersionID, "projects/test-project/secrets/tf-test-secret/versions/",
		"Secret version ID must include the canonical secret path; got %s", secretVersionID)

	secretLabelEnv := outputs.must(t, "secret_label_env")
	require.Equal(t, "dev", secretLabelEnv,
		"Secret Manager labels must round-trip through terraform state; got env=%s", secretLabelEnv)

	subnetID := outputs.must(t, "subnet_id")
	require.Contains(t, subnetID, "projects/test-project/regions/us-central1/subnetworks/tf-test-subnet",
		"subnet id must include the canonical region+name path; got %s", subnetID)

	vpcConnectorID := outputs.must(t, "vpc_access_connector_id")
	require.Contains(t, vpcConnectorID, "projects/test-project/locations/us-central1/connectors/tf-vpc-connector",
		"VPC Access connector id must include the canonical location+name path; got %s", vpcConnectorID)

	firewallID := outputs.must(t, "firewall_id")
	require.Contains(t, firewallID, "projects/test-project/global/firewalls/tf-test-fw-allow-ssh",
		"firewall id must include the canonical global path; got %s", firewallID)

	natAddress := outputs.must(t, "nat_address")
	requireIPv4InCIDR(t, natAddress, "8.34.210.0/24",
		"regional NAT address must come from the simulator's real GCP public IPv4 pool; got %s", natAddress)

	routerNATName := outputs.must(t, "router_nat_name")
	require.Equal(t, "tf-router-nat", routerNATName,
		"router NAT name must round-trip through terraform state; got %s", routerNATName)

	lbHCID := outputs.must(t, "lb_health_check_id")
	require.Contains(t, lbHCID, "projects/test-project/global/healthChecks/tf-lb-hc",
		"load-balancer health check id must include the canonical global path; got %s", lbHCID)

	lbBackendID := outputs.must(t, "lb_backend_service_id")
	require.Contains(t, lbBackendID, "projects/test-project/global/backendServices/tf-lb-backend",
		"load-balancer backend service id must include the canonical global path; got %s", lbBackendID)

	lbURLMapID := outputs.must(t, "lb_url_map_id")
	require.Contains(t, lbURLMapID, "projects/test-project/global/urlMaps/tf-lb-url-map",
		"load-balancer URL map id must include the canonical global path; got %s", lbURLMapID)

	lbProxyID := outputs.must(t, "lb_target_http_proxy_id")
	require.Contains(t, lbProxyID, "projects/test-project/global/targetHttpProxies/tf-lb-http-proxy",
		"load-balancer target HTTP proxy id must include the canonical global path; got %s", lbProxyID)

	lbForwardingIP := outputs.must(t, "lb_forwarding_rule_ip")
	requireIPv4InCIDR(t, lbForwardingIP, "8.34.210.0/24",
		"load-balancer global forwarding rule must come from the simulator's real GCP public IPv4 pool; got %s", lbForwardingIP)

	gcsObjLink := outputs.must(t, "gcs_object_self_link")
	require.Contains(t, gcsObjLink, "tf-test-artifact.txt",
		"GCS object self_link must reference the object name; got %s", gcsObjLink)
	require.Contains(t, gcsObjLink, "tf-test-bucket-",
		"GCS object self_link must reference the parent bucket; got %s", gcsObjLink)

	saEmail := outputs.must(t, "service_account_email")
	require.Equal(t, "tf-test-runner-sa@test-project.iam.gserviceaccount.com", saEmail,
		"service-account email must match the canonical {account_id}@{project}.iam.gserviceaccount.com shape; got %s", saEmail)

	saName := outputs.must(t, "service_account_name")
	require.Equal(t, "projects/test-project/serviceAccounts/tf-test-runner-sa@test-project.iam.gserviceaccount.com", saName,
		"service-account name must include the canonical projects/{project}/serviceAccounts/{email} resource path; got %s", saName)

	// A service-account key is the credential a non-interactive Google client
	// authenticates with, so the provider's key resource must yield a usable
	// one: the canonical {sa}/keys/{keyId} resource name, and a private_key
	// that decodes to the credential file Google issues.
	saKeyName := outputs.must(t, "service_account_key_name")
	require.Contains(t, saKeyName, saName+"/keys/",
		"service-account key name must include the canonical {serviceAccount}/keys/{keyId} path; got %s", saKeyName)
	require.Equal(t, "KEY_ALG_RSA_2048", outputs.must(t, "service_account_key_algorithm"))

	keyFileJSON, err := base64.StdEncoding.DecodeString(outputs.must(t, "service_account_key_private_key"))
	require.NoError(t, err, "private_key must be the base64 credential file IAM returns")
	var keyFile map[string]string
	require.NoError(t, json.Unmarshal(keyFileJSON, &keyFile),
		"the decoded private_key must be the JSON credential file: %s", keyFileJSON)
	require.Equal(t, "service_account", keyFile["type"])
	require.Equal(t, "test-project", keyFile["project_id"])
	require.Equal(t, "tf-test-runner-sa@test-project.iam.gserviceaccount.com", keyFile["client_email"])
	require.Contains(t, keyFile["private_key"], "BEGIN PRIVATE KEY",
		"the credential file must carry a PEM private key a client can sign an assertion with")
	require.Equal(t, saKeyName[strings.LastIndex(saKeyName, "/")+1:], keyFile["private_key_id"],
		"the credential file's private_key_id must be the key id the resource name carries")
	require.NotEmpty(t, keyFile["token_uri"], "the credential file must name the token endpoint to exchange the assertion at")

	bigtableInstanceID := outputs.must(t, "bigtable_instance_id")
	require.Contains(t, bigtableInstanceID, "projects/test-project/instances/tf-bt1",
		"Bigtable instance id must include the canonical project instance path; got %s", bigtableInstanceID)

	bigtableTableID := outputs.must(t, "bigtable_table_id")
	require.Contains(t, bigtableTableID, "projects/test-project/instances/tf-bt1/tables/app_table",
		"Bigtable table id must include the canonical project instance/table path; got %s", bigtableTableID)

	bqTableID := outputs.must(t, "bigquery_table_id")
	require.Contains(t, bqTableID, "/datasets/tf_test_dataset/tables/events",
		"BigQuery table id must include dataset/table; got %s", bqTableID)

	bqTableLabelEnv := outputs.must(t, "bigquery_table_label_env")
	require.Equal(t, "terraform", bqTableLabelEnv,
		"BigQuery table labels must round-trip through terraform state; got env=%s", bqTableLabelEnv)

	fsDocName := outputs.must(t, "firestore_document_name")
	require.Contains(t, fsDocName, "projects/test-project/databases/(default)/documents/tf-users/alice",
		"Firestore document name must round-trip the canonical document path; got %s", fsDocName)

	redisID := outputs.must(t, "redis_instance_id")
	require.Contains(t, redisID, "projects/test-project/locations/us-central1/instances/tf-redis",
		"Memorystore Redis id must round-trip the full resource path; got %s", redisID)

	redisHost := outputs.must(t, "redis_instance_host")
	require.True(t, strings.HasPrefix(redisHost, "10."),
		"Memorystore Redis host must be an RFC1918 address; got %s", redisHost)

	sqlConnectionName := outputs.must(t, "sql_instance_connection_name")
	require.Equal(t, "test-project:us-central1:tf-sql", sqlConnectionName,
		"Cloud SQL connection name must match project:region:instance; got %s", sqlConnectionName)

	sqlDatabaseID := outputs.must(t, "sql_database_id")
	require.Contains(t, sqlDatabaseID, "projects/test-project/instances/tf-sql/databases/appdb",
		"Cloud SQL database id must include instance and database name; got %s", sqlDatabaseID)

	sqlUserName := outputs.must(t, "sql_user_name")
	require.Equal(t, "appuser", sqlUserName,
		"Cloud SQL user name must round-trip through terraform state; got %s", sqlUserName)

	spannerInstanceID := outputs.must(t, "spanner_instance_id")
	require.Equal(t, "test-project/tf-spanner", spannerInstanceID,
		"Terraform google_spanner_instance.id follows the provider's {{project}}/{{name}} format")

	spannerDatabaseID := outputs.must(t, "spanner_database_id")
	require.Equal(t, "tf-spanner/appdb", spannerDatabaseID,
		"Terraform google_spanner_database.id follows the provider's {{instance}}/{{name}} format")

	projectID := outputs.must(t, "project_id")
	require.Equal(t, "tf-created-project", projectID,
		"google_project must round-trip its project_id through the v1 create + read")

	projectNumber := outputs.must(t, "project_number")
	require.Regexp(t, `^[1-9][0-9]{11}$`, projectNumber,
		"google_project.number must be the real 12-digit project number the v1 read returns; got %s", projectNumber)

	folderName := outputs.must(t, "folder_name")
	require.Regexp(t, `^folders/[1-9][0-9]{11}$`, folderName,
		"google_folder.name must be the folder resource name the create operation settled on; got %s", folderName)
	require.Equal(t, "organizations/123456789012", outputs.must(t, "folder_parent"))

	require.Equal(t, "constraints/iam.disableServiceAccountKeyCreation",
		outputs.must(t, "project_org_policy_constraint"),
		"google_project_organization_policy round-trips its constraint through setOrgPolicy + getOrgPolicy")
	require.Equal(t, true, outputs.mustValue(t, "project_org_policy_enforced"),
		"the boolean policy reads back enforced")
	// etag and update_time have no counterpart in configuration, so they come
	// from getOrgPolicy alone: a policy the API did not store reads back with
	// neither.
	require.NotEmpty(t, outputs.must(t, "project_org_policy_etag"),
		"the stored policy must carry the etag its write returned")
	require.Regexp(t, `^\d{4}-\d{2}-\d{2}T`, outputs.must(t, "project_org_policy_update_time"),
		"the stored policy must carry the RFC3339 timestamp of its write")
	require.Equal(t, []any{"in:us-locations"},
		outputs.mustValue(t, "folder_org_policy_allowed_values"),
		"google_folder_organization_policy round-trips its allowed values")
	require.Equal(t, "constraints/iam.disableServiceAccountCreation",
		outputs.must(t, "organization_org_policy_constraint"))
	// Written enforced = false, against the project policy's enforced = true:
	// the pair separates a stored boolean policy from a constant answer.
	require.Equal(t, false, outputs.mustValue(t, "organization_org_policy_enforced"),
		"an unenforced boolean policy must read back unenforced")

	// The provider normalizes the API's "liens/{id}" resource name down to the
	// bare id in state, so what a lien round-trips through terraform is the
	// identifier — the wire shape itself is asserted in the SDK suite.
	lienName := outputs.must(t, "resource_manager_lien_name")
	require.Regexp(t, `^[0-9]+$`, lienName,
		"google_resource_manager_lien.name must be the lien identifier the v1 create returned; got %s", lienName)

	out, err = runTimed(t, "terraform destroy", terraformCmd("destroy", "-auto-approve", "-var", "secret_label_env=dev"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
}

func requireTerraformNetworkHost(t *testing.T) {
	t.Helper()
	require.NoError(t, realexec.DetectNetworkCapabilities().Require(),
		"Terraform Compute/Network coverage required a privileged Linux host with iproute2 and nftables; use `make terraform-test`, which selected the real-network container harness on macOS")
}

func requireIPv4InCIDR(t *testing.T, value, cidr string, msgAndArgs ...any) {
	t.Helper()
	ip := net.ParseIP(value).To4()
	require.NotNil(t, ip, msgAndArgs...)
	_, block, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	require.True(t, block.Contains(ip), msgAndArgs...)
}

// cleanTerraformArtifacts empties a terraform working directory of everything a
// previous run put there: the provider cache and lock file, the state and its
// backup, and any crash log. A run aborted before its destroy leaves state
// naming resources this simulator process has never heard of, which turns the
// next apply into an update against phantom resources and the next destroy into
// a failure — so every suite starts from an empty workspace rather than
// inheriting one.
func cleanTerraformArtifacts(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{
		".terraform",
		".terraform.lock.hcl",
		"terraform.tfstate",
		"terraform.tfstate.backup",
		"crash.log",
	} {
		err := os.RemoveAll(filepath.Join(dir, name))
		require.NoError(t, err, "clean terraform workspace artifact %s", name)
	}
}

// cleanTerraformWorkspace empties the root working directory, the one main.tf
// lives in.
func cleanTerraformWorkspace(t *testing.T) {
	t.Helper()
	cleanTerraformArtifacts(t, filepath.Dir(mustAbs("main.tf")))
}

// cleanTerraformFixture empties a fixture working directory, named relative to
// the root the way terraformCmdInDir names one.
func cleanTerraformFixture(t *testing.T, dir string) {
	t.Helper()
	cleanTerraformArtifacts(t, filepath.Join(filepath.Dir(mustAbs("main.tf")), dir))
}

type tfOutputs map[string]struct {
	Sensitive bool        `json:"sensitive"`
	Type      interface{} `json:"type"`
	Value     interface{} `json:"value"`
}

func (o tfOutputs) must(t *testing.T, key string) string {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	s, ok := v.Value.(string)
	require.True(t, ok, "output %q is not a string (got %T)", key, v.Value)
	require.NotEmpty(t, s, "output %q is empty", key)
	return s
}

// mustValue reads an output of any type, for the booleans and lists a string
// or number accessor cannot carry.
func (o tfOutputs) mustValue(t *testing.T, key string) any {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	return v.Value
}

// mustNumber reads a numeric output (terraform encodes every number as a JSON
// float).
func (o tfOutputs) mustNumber(t *testing.T, key string) float64 {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	n, ok := v.Value.(float64)
	require.True(t, ok, "output %q is not a number (got %T)", key, v.Value)
	return n
}

// mustObject reads an object-typed output — a nested configuration block such
// as a container probe or an environment variable — as a decoded map.
func (o tfOutputs) mustObject(t *testing.T, key string) map[string]any {
	t.Helper()
	v, ok := o[key]
	require.True(t, ok, "output %q missing from terraform state", key)
	m, ok := v.Value.(map[string]any)
	require.True(t, ok, "output %q is not an object (got %T)", key, v.Value)
	return m
}

// mustBlock reads the single element of a max_items=1 nested block that
// terraform represents as a one-element list (probe handlers, value_source,
// secret_key_ref).
func mustBlock(t *testing.T, obj map[string]any, key string) map[string]any {
	t.Helper()
	list, ok := obj[key].([]any)
	require.True(t, ok, "block %q is not a list (got %T)", key, obj[key])
	require.Len(t, list, 1, "block %q must carry exactly one element", key)
	m, ok := list[0].(map[string]any)
	require.True(t, ok, "block %q element is not an object (got %T)", key, list[0])
	return m
}

func readOutputs(t *testing.T) tfOutputs {
	t.Helper()
	out, err := terraformCmd("output", "-json").CombinedOutput()
	require.NoError(t, err, "terraform output failed:\n%s", out)
	var outputs tfOutputs
	require.NoError(t, json.Unmarshal(out, &outputs))
	return outputs
}
