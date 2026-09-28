variable "region" {
  type    = string
  default = "us-east-1"
}

variable "sleeper_username" {
  type = string
}

variable "espn_league_ids" {
  type    = list(string)
  default = []
}

variable "ssm_api_key_param" {
  type    = string
  default = "/sports-api/api-key"
  validation {
    condition     = startswith(var.ssm_api_key_param, "/")
    error_message = "SSM parameter names must start with /."
  }
}

variable "ssm_espn_s2_param" {
  type    = string
  default = "/sports-api/espn-s2"
  validation {
    condition     = startswith(var.ssm_espn_s2_param, "/")
    error_message = "SSM parameter names must start with /."
  }
}

variable "ssm_espn_swid_param" {
  type    = string
  default = "/sports-api/espn-swid"
  validation {
    condition     = startswith(var.ssm_espn_swid_param, "/")
    error_message = "SSM parameter names must start with /."
  }
}

variable "lambda_zip" {
  type    = string
  default = "../../dist/bootstrap.zip"
}
