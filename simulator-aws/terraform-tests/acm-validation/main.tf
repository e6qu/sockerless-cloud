terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.71.0"
    }
  }
}

variable "endpoint" {
  type = string
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    acm     = var.endpoint
    route53 = var.endpoint
  }
}

# A DNS-validated certificate with a wildcard SAN, whose validation record
# names carry no literal '*'.
resource "aws_acm_certificate" "tf_cert" {
  domain_name               = "app.example.test"
  subject_alternative_names = ["*.devbox.example.test"]
  validation_method         = "DNS"

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_route53_zone" "tf_zone" {
  name = "example.test"
}

resource "aws_route53_record" "tf_validation" {
  for_each = {
    for dvo in aws_acm_certificate.tf_cert.domain_validation_options : dvo.domain_name => {
      name   = dvo.resource_record_name
      type   = dvo.resource_record_type
      record = dvo.resource_record_value
    }
  }

  zone_id = aws_route53_zone.tf_zone.zone_id
  name    = each.value.name
  type    = each.value.type
  ttl     = 60
  records = [each.value.record]
}

# Waits for the certificate to reach ISSUED.
resource "aws_acm_certificate_validation" "tf_cert_val" {
  certificate_arn         = aws_acm_certificate.tf_cert.arn
  validation_record_fqdns = [for r in aws_route53_record.tf_validation : r.fqdn]

  timeouts {
    create = "120s"
  }
}

output "certificate_arn" {
  value = aws_acm_certificate.tf_cert.arn
}

output "validation_id" {
  value = aws_acm_certificate_validation.tf_cert_val.id
}

output "validation_record_names" {
  value = join(",", sort([for r in aws_route53_record.tf_validation : r.name]))
}
