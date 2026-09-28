variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

# The handler image is built for the machine running the suite, so the
# function declares that machine's architecture.
variable "lambda_architecture" {
  description = "Lambda architecture of the locally built handler image"
  type        = string
  default     = "x86_64"
}
