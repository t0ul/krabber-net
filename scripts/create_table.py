#!/usr/bin/env python3
"""Create Krabber's DynamoDB table.

The same table that Terraform (infra/modules/table) and the Go code
(internal/store/schema.go, used for DynamoDB Local) define. A Go test
(TestPythonSchemaMatches) runs this script with --print and fails if the two
ever disagree.

Usage:
    python3 scripts/create_table.py --table krabber-prod            # AWS (default profile/region)
    python3 scripts/create_table.py --table krabber-dev --local     # DynamoDB Local on :8000
    python3 scripts/create_table.py --table krabber-prod --print    # just print the definition

Needs boto3 (pip install boto3) except with --print.
"""

import argparse
import json
import sys

REGION = "us-east-2"

# Every key format lives in internal/store/keys.go; see PLAN.md section 4.2
# for what each index is for.
#
#   GSI2  ID#<crabID>            crab by ID, list crabs (the directory)
#   GSI3  M#<yyyy-mm-dd>/M#<id>  the day's molts, newest first (the Sea)
#   GSI5  M#<moltID>             molt by ID
#   GSI6  F#<followeeID>         a crab's followers (trench fan-out)
#   GSI7  L#<moltID>             who liked a molt
#   GSI8  Q#<queue>              sparse work queue: pending fan-out, purges, open reports
#
# The molt indexes only find keys; every read then loads the molt from the
# base table, so they project KEYS_ONLY and a like or reply doesn't rewrite
# two index copies of the molt.
INDEXES = [
    ("GSI2", "ALL"),
    ("GSI3", "KEYS_ONLY"),
    ("GSI5", "KEYS_ONLY"),
    ("GSI6", "ALL"),
    ("GSI7", "ALL"),
    ("GSI8", "ALL"),
]

# On-demand maximum throughput for the launch stage (PLAN.md section 4.1):
# requests above these are throttled, not billed, which caps the DynamoDB
# bill. Raise them with MAX_KRABS. The app paces its own bursts to stay under
# them: the hourly directory scan of GSI2 at 100 reads/s and trench fan-out
# at 25 writes/s.
TABLE_MAX_READS, TABLE_MAX_WRITES = 50, 40
INDEX_MAX = {  # reads/s, writes/s
    "GSI2": (150, 10),
    "GSI3": (25, 10),
    "GSI5": (25, 10),
    "GSI6": (25, 10),
    "GSI7": (25, 10),
    "GSI8": (10, 5),
}


def create_table_input(table, caps=True):
    """The CreateTable request for the Krabber table."""
    attrs = [
        {"AttributeName": "PK", "AttributeType": "S"},
        {"AttributeName": "SK", "AttributeType": "S"},
    ]
    gsis = []
    for name, projection in INDEXES:
        attrs += [
            {"AttributeName": name + "PK", "AttributeType": "S"},
            {"AttributeName": name + "SK", "AttributeType": "S"},
        ]
        gsi = {
            "IndexName": name,
            "KeySchema": [
                {"AttributeName": name + "PK", "KeyType": "HASH"},
                {"AttributeName": name + "SK", "KeyType": "RANGE"},
            ],
            "Projection": {"ProjectionType": projection},
        }
        if caps:
            reads, writes = INDEX_MAX[name]
            gsi["OnDemandThroughput"] = {
                "MaxReadRequestUnits": reads,
                "MaxWriteRequestUnits": writes,
            }
        gsis.append(gsi)
    req = {
        "TableName": table,
        "BillingMode": "PAY_PER_REQUEST",
        "AttributeDefinitions": attrs,
        "KeySchema": [
            {"AttributeName": "PK", "KeyType": "HASH"},
            {"AttributeName": "SK", "KeyType": "RANGE"},
        ],
        "GlobalSecondaryIndexes": gsis,
    }
    if caps:
        req["OnDemandThroughput"] = {
            "MaxReadRequestUnits": TABLE_MAX_READS,
            "MaxWriteRequestUnits": TABLE_MAX_WRITES,
        }
        req["DeletionProtectionEnabled"] = True
    return req


def create(client, table, local):
    from botocore.exceptions import ClientError

    try:
        client.create_table(**create_table_input(table, caps=not local))
    except ClientError as e:
        if e.response["Error"]["Code"] == "ResourceInUseException":
            print(f"{table} already exists; leaving it as it is")
            return
        raise
    print(f"creating {table}...")
    client.get_waiter("table_exists").wait(TableName=table)
    client.update_time_to_live(
        TableName=table,
        TimeToLiveSpecification={"AttributeName": "expires_at", "Enabled": True},
    )
    if not local:
        client.update_continuous_backups(
            TableName=table,
            PointInTimeRecoverySpecification={"PointInTimeRecoveryEnabled": True},
        )
    print(f"{table} ready: TTL on expires_at" + ("" if local else ", point-in-time recovery on"))


def main():
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--table", required=True, help="table name, e.g. krabber-prod or krabber-dev")
    p.add_argument("--region", default=REGION)
    p.add_argument("--local", nargs="?", const="http://localhost:8000", metavar="ENDPOINT",
                   help="use DynamoDB Local (default endpoint http://localhost:8000); no caps, PITR or deletion protection")
    p.add_argument("--print", action="store_true", dest="print_only",
                   help="print the CreateTable request as JSON and exit")
    args = p.parse_args()

    if args.print_only:
        json.dump(create_table_input(args.table, caps=not args.local), sys.stdout, indent=2)
        print()
        return

    import boto3

    if args.local:
        client = boto3.client("dynamodb", region_name=args.region, endpoint_url=args.local,
                              aws_access_key_id="local", aws_secret_access_key="local")
    else:
        client = boto3.client("dynamodb", region_name=args.region)
    create(client, args.table, bool(args.local))


if __name__ == "__main__":
    main()
