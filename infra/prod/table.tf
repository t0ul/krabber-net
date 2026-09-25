# The single table (PLAN.md section 4). Keys and projections match
# internal/store/schema.go and scripts/create_table.py.

locals {
  index_projections = {
    GSI2 = "ALL"
    GSI3 = "KEYS_ONLY"
    GSI5 = "KEYS_ONLY"
    GSI6 = "ALL"
    GSI7 = "ALL"
    GSI8 = "ALL"
  }
}

resource "aws_dynamodb_table" "main" {
  name         = local.env_name
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "PK"
  range_key    = "SK"

  on_demand_throughput {
    max_read_request_units  = var.table_caps.table.reads
    max_write_request_units = var.table_caps.table.writes
  }

  attribute {
    name = "PK"
    type = "S"
  }
  attribute {
    name = "SK"
    type = "S"
  }

  dynamic "attribute" {
    for_each = toset(flatten([for name in keys(local.index_projections) : ["${name}PK", "${name}SK"]]))
    content {
      name = attribute.value
      type = "S"
    }
  }

  dynamic "global_secondary_index" {
    for_each = local.index_projections
    content {
      name            = global_secondary_index.key
      hash_key        = "${global_secondary_index.key}PK"
      range_key       = "${global_secondary_index.key}SK"
      projection_type = global_secondary_index.value

      on_demand_throughput {
        max_read_request_units  = var.table_caps.indexes[global_secondary_index.key].reads
        max_write_request_units = var.table_caps.indexes[global_secondary_index.key].writes
      }
    }
  }

  ttl {
    attribute_name = "expires_at"
    enabled        = true
  }

  point_in_time_recovery {
    enabled = true
  }

  deletion_protection_enabled = true

  lifecycle {
    prevent_destroy = true
  }
}
