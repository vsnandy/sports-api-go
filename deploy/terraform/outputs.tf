output "api_url" {
  value = aws_apigatewayv2_api.api.api_endpoint
}

output "players_bucket" {
  value = aws_s3_bucket.players.bucket
}
