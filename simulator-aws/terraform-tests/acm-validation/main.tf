terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "6.50.0"
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

# DNS-validated cert with a wildcard SAN — the consumer's control-plane +
# workspace-wildcard shape that hit both failures: the cert never reaching ISSUED, and
# the validation record name keeping a literal '*'.
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

# One _acm-challenge record per (de-wildcarded) validation option.
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

# Waits for the cert to reach ISSUED — hung forever before the fix.
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
