variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "access_token" {
  description = "OAuth2 access token the provider presents to the simulator data plane; minted by the test harness from the simulator's token endpoint."
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
