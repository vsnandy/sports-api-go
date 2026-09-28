variable "region" {
  type    = string
  default = "us-east-1"
}

variable "github_repo" {
  description = "owner/name of the GitHub repository allowed to assume the CI roles"
  type        = string
  default     = "vsnandy/sports-api-go"
  validation {
    condition     = can(regex("^[^/]+/[^/]+$", var.github_repo))
    error_message = "github_repo must look like owner/name."
  }
}

variable "create_oidc_provider" {
  description = "false when this account already has a token.actions.githubusercontent.com OIDC provider"
  type        = bool
  default     = true
}

variable "ssm_prefix" {
  description = "SSM parameter path the Lambda and the smoke test may read"
  type        = string
  default     = "/sports-api/"
  validation {
    condition     = startswith(var.ssm_prefix, "/") && endswith(var.ssm_prefix, "/")
    error_message = "ssm_prefix must start and end with /."
  }
}
