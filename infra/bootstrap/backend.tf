terraform {
  backend "s3" {
    bucket       = "krabber-tfstate-381466680812"
    key          = "bootstrap/terraform.tfstate"
    region       = "us-east-2"
    encrypt      = true
    use_lockfile = true
  }
}
