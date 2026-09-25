package store

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
)

// TestPythonSchemaMatches keeps scripts/create_table.py in step with
// CreateTableInput: same keys, attributes, indexes and projections.
func TestPythonSchemaMatches(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not installed")
	}
	out, err := exec.Command(python, filepath.Join("..", "..", "scripts", "create_table.py"), "--table", "krabber-test", "--local", "--print").Output()
	if err != nil {
		t.Fatalf("create_table.py --print: %v", err)
	}
	var py struct {
		TableName              string
		BillingMode            string
		AttributeDefinitions   []struct{ AttributeName, AttributeType string }
		KeySchema              []struct{ AttributeName, KeyType string }
		GlobalSecondaryIndexes []struct {
			IndexName  string
			KeySchema  []struct{ AttributeName, KeyType string }
			Projection struct{ ProjectionType string }
		}
	}
	if err := json.Unmarshal(out, &py); err != nil {
		t.Fatalf("parse: %v\n%s", err, out)
	}

	goIn := CreateTableInput("krabber-test")
	if py.TableName != aws.ToString(goIn.TableName) || py.BillingMode != string(goIn.BillingMode) {
		t.Errorf("table: python %s/%s, go %s/%s", py.TableName, py.BillingMode, aws.ToString(goIn.TableName), goIn.BillingMode)
	}

	goAttrs := map[string]string{}
	for _, a := range goIn.AttributeDefinitions {
		goAttrs[aws.ToString(a.AttributeName)] = string(a.AttributeType)
	}
	pyAttrs := map[string]string{}
	for _, a := range py.AttributeDefinitions {
		pyAttrs[a.AttributeName] = a.AttributeType
	}
	if len(goAttrs) != len(pyAttrs) {
		t.Errorf("attributes: python %v, go %v", pyAttrs, goAttrs)
	}
	for k, v := range goAttrs {
		if pyAttrs[k] != v {
			t.Errorf("attribute %s: python %q, go %q", k, pyAttrs[k], v)
		}
	}

	if len(py.KeySchema) != len(goIn.KeySchema) {
		t.Fatalf("key schema: python %v", py.KeySchema)
	}
	for i, k := range goIn.KeySchema {
		if py.KeySchema[i].AttributeName != aws.ToString(k.AttributeName) || py.KeySchema[i].KeyType != string(k.KeyType) {
			t.Errorf("key %d: python %+v, go %s %s", i, py.KeySchema[i], aws.ToString(k.AttributeName), k.KeyType)
		}
	}

	if len(py.GlobalSecondaryIndexes) != len(goIn.GlobalSecondaryIndexes) {
		t.Fatalf("indexes: python %d, go %d", len(py.GlobalSecondaryIndexes), len(goIn.GlobalSecondaryIndexes))
	}
	for i, g := range goIn.GlobalSecondaryIndexes {
		p := py.GlobalSecondaryIndexes[i]
		if p.IndexName != aws.ToString(g.IndexName) || p.Projection.ProjectionType != string(g.Projection.ProjectionType) {
			t.Errorf("index %d: python %s/%s, go %s/%s", i, p.IndexName, p.Projection.ProjectionType, aws.ToString(g.IndexName), g.Projection.ProjectionType)
		}
		for j, k := range g.KeySchema {
			if p.KeySchema[j].AttributeName != aws.ToString(k.AttributeName) || p.KeySchema[j].KeyType != string(k.KeyType) {
				t.Errorf("index %s key %d: python %+v", p.IndexName, j, p.KeySchema[j])
			}
		}
	}
}
