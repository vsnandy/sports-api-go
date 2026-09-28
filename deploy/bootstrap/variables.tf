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

# GitHub's OIDC subject embeds immutable IDs: repo:<owner>@<owner_id>/<name>@<repo_id>:...
# Find them with `curl -s https://api.github.com/repos/<owner>/<name> | jq '.owner.id, .id'`.
variable "github_owner_id" {
  description = "numeric GitHub ID of the repository owner"
  type        = string
  default     = "3279133"
  validation {
    condition     = can(regex("^[0-9]+$", var.github_owner_id))
    error_message = "github_owner_id must be numeric."
  }
}

variable "github_repo_id" {
  description = "numeric GitHub ID of the repository"
  type        = string
  default     = "1388294779"
  validation {
    condition     = can(regex("^[0-9]+$", var.github_repo_id))
    error_message = "github_repo_id must be numeric."
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
