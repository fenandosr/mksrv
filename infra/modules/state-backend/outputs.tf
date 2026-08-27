output "backend" {
  value = {
    bucket         = var.bucket
    dynamodb_table = var.dynamodb_table
    region         = var.region
  }
}
