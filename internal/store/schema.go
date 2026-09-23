package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

// Indexes lists the GSIs the code uses. infra/modules/table must define the
// same set.
var Indexes = []string{gsiCrabByID, gsiMoltsByDay, gsiMoltByID, gsiFollowers, gsiLikesOnMolt, gsiWorkQueue}

// CreateTableInput describes the table for DynamoDB Local (dev and tests).
func CreateTableInput(table string) *dynamodb.CreateTableInput {
	attrs := []types.AttributeDefinition{
		{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS},
		{AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS},
	}
	var gsis []types.GlobalSecondaryIndex
	for _, name := range Indexes {
		pk, sk := name+"PK", name+"SK"
		attrs = append(attrs,
			types.AttributeDefinition{AttributeName: aws.String(pk), AttributeType: types.ScalarAttributeTypeS},
			types.AttributeDefinition{AttributeName: aws.String(sk), AttributeType: types.ScalarAttributeTypeS},
		)
		gsis = append(gsis, types.GlobalSecondaryIndex{
			IndexName: aws.String(name),
			KeySchema: []types.KeySchemaElement{
				{AttributeName: aws.String(pk), KeyType: types.KeyTypeHash},
				{AttributeName: aws.String(sk), KeyType: types.KeyTypeRange},
			},
			Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll},
		})
	}
	return &dynamodb.CreateTableInput{
		TableName:            aws.String(table),
		BillingMode:          types.BillingModePayPerRequest,
		AttributeDefinitions: attrs,
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange},
		},
		GlobalSecondaryIndexes: gsis,
	}
}

// EnsureTable creates the table and its TTL setting if they don't exist.
func EnsureTable(ctx context.Context, db *dynamodb.Client, table string) error {
	_, err := db.CreateTable(ctx, CreateTableInput(table))
	var inUse *types.ResourceInUseException
	switch {
	case errors.As(err, &inUse):
		return nil
	case err != nil:
		return fmt.Errorf("create table %s: %w", table, err)
	}
	if err := dynamodb.NewTableExistsWaiter(db).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(table)}, time.Minute); err != nil {
		return fmt.Errorf("wait for table %s: %w", table, err)
	}
	_, err = db.UpdateTimeToLive(ctx, &dynamodb.UpdateTimeToLiveInput{
		TableName: aws.String(table),
		TimeToLiveSpecification: &types.TimeToLiveSpecification{
			AttributeName: aws.String("expires_at"),
			Enabled:       aws.Bool(true),
		},
	})
	if err != nil {
		return fmt.Errorf("enable TTL on %s: %w", table, err)
	}
	return nil
}
