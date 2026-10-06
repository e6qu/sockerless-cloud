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

# The test pushes the image outside Terraform after the first apply, then
# applies again with read_image set to read it back.
variable "read_image" {
  type    = bool
  default = false
}

provider "aws" {
  region                      = "us-east-1"
  access_key                  = "test"
  secret_key                  = "test"
  skip_credentials_validation = true
  skip_requesting_account_id  = true

  endpoints {
    ecr = var.endpoint
  }
}

resource "aws_ecr_repository" "app" {
  name         = "tf-ecr-image-size"
  force_delete = true
}

data "aws_ecr_image" "pushed" {
  count           = var.read_image ? 1 : 0
  repository_name = aws_ecr_repository.app.name
  image_tag       = "v1"
}

output "image_size_in_bytes" {
  value = var.read_image ? tostring(data.aws_ecr_image.pushed[0].image_size_in_bytes) : ""
}
