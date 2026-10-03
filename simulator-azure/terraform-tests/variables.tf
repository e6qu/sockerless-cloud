variable "endpoint" {
  description = "Simulator endpoint URL"
  type        = string
}

variable "node_app_package_url" {
  description = "URL of the zip package the Node web app runs from"
  type        = string
}

variable "zip_app_package_path" {
  description = "Local path of the zip package the zip-deployed web app is deployed from"
  type        = string
}

variable "node_function_package_url" {
  description = "URL of the zip package the Node function app runs from"
  type        = string
}
