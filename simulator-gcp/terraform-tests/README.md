# simulator-gcp-terraform-tests

Integration tests that run `terraform apply` and `terraform destroy` against the GCP simulator. Verifies that the simulator implements enough of the GCP API surface for the Terraform Google provider to provision and tear down resources.

Resources covered:
- `google_compute_network` + `google_compute_disk` + `google_compute_subnetwork` + `google_compute_firewall`
- `google_dns_managed_zone` (public + private) + `google_dns_record_set`
- `google_artifact_registry_repository` (Docker)
- `google_cloud_run_v2_service` + `google_cloud_run_v2_job`
- `google_cloudfunctions2_function`
- `google_eventarc_trigger`
- `google_eventarc_channel`
- `google_pubsub_topic` + `google_pubsub_subscription`
- `google_cloudbuild_trigger`
- `google_storage_bucket` + `google_storage_bucket_object`
- `google_vpc_access_connector`
- `google_logging_project_sink` + `google_logging_metric`
- `google_bigquery_dataset` + `google_bigquery_table`
- `google_firestore_document`
- `google_redis_instance`
- `google_sql_database_instance` + `google_sql_database` + `google_sql_user`
- `google_bigtable_instance` + `google_bigtable_table`
- `google_secret_manager_secret` + `google_secret_manager_secret_version`
- `google_service_account` (via `iam_beta_custom_endpoint`)
- `google_service_account_key` (via `iam_custom_endpoint`)

`google_compute_instance` is covered by the cross-cloud VM compute parity phase. Instance templates remain out of the foundational slice until a real sockerless flow or provider path requires them.

## Running

```sh
cd simulator-gcp/terraform-tests
go test -v ./...
```

To run the same provider flow through the optional Caddy HTTPS gateway:

```sh
cd simulator-gcp
make terraform-https-test
```

The HTTPS target uses Caddy's `https://localhost:<ephemeral-port>` single-simulator route so the test does not depend on wildcard `.localhost` DNS support. It still uses Caddy TLS and passes the generated root CA to Terraform through `SSL_CERT_FILE`. On macOS the Make target runs the same test inside the shared Linux simulator test image so the real provider honors that CA file.

The test harness (`helpers_test.go`) handles simulator binary build, port allocation, server startup, Terraform init/apply/destroy, and shutdown. It exports `BIGTABLE_EMULATOR_HOST` because the Google provider uses Bigtable Admin's official gRPC emulator route for Terraform resources. No external services required.

## Prerequisites

- Go 1.23+
- `terraform` CLI installed and on `PATH`
- The `simulator-gcp/` parent module (built automatically by `TestMain`)
- `caddy` installed and on `PATH` for `make terraform-https-test`

## How it works

1. `TestMain` builds the GCP simulator binary and starts it on a free port
2. Tests write Terraform configurations to a temp directory
3. `terraform init` downloads the Google provider
4. `terraform apply -auto-approve` provisions resources against the simulator
5. Test assertions verify the Terraform state
6. `terraform destroy -auto-approve` tears down resources
