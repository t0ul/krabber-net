package platform

import (
	"context"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestMeter(t *testing.T) {
	endpoint := os.Getenv("KRABBER_TEST_DYNAMO_ENDPOINT")
	if endpoint == "" {
		t.Skip("KRABBER_TEST_DYNAMO_ENDPOINT not set; skipping DynamoDB Local test")
	}
	ctx := context.Background()
	cfg, err := AWSConfig(ctx, "us-east-2")
	if err != nil {
		t.Fatal(err)
	}
	db := DynamoDB(cfg, endpoint)
	table := "krabber-metertest"
	_, _ = db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	if _, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:   aws.String(table),
		BillingMode: types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{
			{AttributeName: aws.String("PK"), AttributeType: types.ScalarAttributeTypeS},
			{AttributeName: aws.String("SK"), AttributeType: types.ScalarAttributeTypeS},
		},
		KeySchema: []types.KeySchemaElement{
			{AttributeName: aws.String("PK"), KeyType: types.KeyTypeHash},
			{AttributeName: aws.String("SK"), KeyType: types.KeyTypeRange},
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
	})

	key := func(pk, sk string) map[string]types.AttributeValue {
		return map[string]types.AttributeValue{"PK": &types.AttributeValueMemberS{Value: pk}, "SK": &types.AttributeValueMemberS{Value: sk}}
	}
	mctx, m := WithMeter(ctx)
	if _, err := db.PutItem(mctx, &dynamodb.PutItemInput{TableName: aws.String(table), Item: key("a", "1")}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetItem(mctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: key("a", "1"), ConsistentRead: aws.Bool(true)}); err != nil {
		t.Fatal(err)
	}
	var missing []map[string]types.AttributeValue
	for _, sk := range []string{"x", "y", "z", "w"} {
		missing = append(missing, key("a", sk))
	}
	if _, err := db.BatchGetItem(mctx, &dynamodb.BatchGetItemInput{RequestItems: map[string]types.KeysAndAttributes{table: {Keys: missing}}}); err != nil {
		t.Fatal(err)
	}
	// One consistent read, plus half a unit for each of the 4 missing items.
	if read, write, calls := m.Units(); calls != 3 || write != 1 || read != 3 {
		t.Errorf("read %v write %v calls %d", read, write, calls)
	}

	// Calls without a meter aren't changed.
	out, err := db.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(table), Key: key("a", "1")})
	if err != nil || out.ConsumedCapacity != nil {
		t.Errorf("unmetered call reported capacity: %+v %v", out.ConsumedCapacity, err)
	}
}
