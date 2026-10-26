variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "bigtable_endpoint" {
  description = "The simulator's gRPC port as a TLS front end, serving the Bigtable admin and data gRPC services and the REST surface on one host, as bigtableadmin.googleapis.com does."
  type        = string
}

variable "credentials" {
  description = "The service-account credential file the provider authenticates with, by path or by contents; the test harness writes the simulator's bootstrap key."
  type        = string
}

variable "secret_label_env" {
  description = "Secret Manager label value used to exercise UpdateSecret."
  type        = string
  default     = "dev"
}

variable "worker_image" {
  description = "Image the Cloud Run worker pool's containers run: an HTTP server on 9090 (`server`) and on 8080 (`probe`), so its instances start and pass their startup probe."
  type        = string
}
