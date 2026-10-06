terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.67.0"
    }
  }
}

variable "endpoint" {
  type = string
}

# The PEM CA certificates bundle and certificate revocation list the test
# generates.
variable "ca_bundle" {
  type = string
}

variable "crl" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    s3    = var.endpoint
    elbv2 = var.endpoint
  }
}

resource "aws_s3_bucket" "ca" {
  bucket        = "tf-elb-trust-store-ca"
  force_destroy = true
}

resource "aws_s3_object" "bundle" {
  bucket  = aws_s3_bucket.ca.id
  key     = "bundle.pem"
  content = var.ca_bundle
}

resource "aws_s3_object" "crl" {
  bucket  = aws_s3_bucket.ca.id
  key     = "crl.pem"
  content = var.crl
}

resource "aws_lb_trust_store" "mtls" {
  name                             = "tf-mtls"
  ca_certificates_bundle_s3_bucket = aws_s3_bucket.ca.id
  ca_certificates_bundle_s3_key    = aws_s3_object.bundle.key
}

resource "aws_lb_trust_store_revocation" "crl" {
  trust_store_arn       = aws_lb_trust_store.mtls.arn
  revocations_s3_bucket = aws_s3_bucket.ca.id
  revocations_s3_key    = aws_s3_object.crl.key
}

output "trust_store_arn" {
  value = aws_lb_trust_store.mtls.arn
}

output "revocation_id" {
  value = tostring(aws_lb_trust_store_revocation.crl.revocation_id)
}
