package platform

import (
	"context"
	"sync"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/smithy-go/middleware"
)

// Meter adds up the DynamoDB capacity used under one context, such as one web
// request, so the request log shows what each page costs. Each call counts at
// least what AWS bills for it even when nothing is found (DynamoDB Local
// reports reads of missing items as free; AWS doesn't). Writes that fail a
// condition are billed but not reported, so they aren't counted.
type Meter struct {
	mu          sync.Mutex
	read, write float64
	calls       int
}

type meterKey struct{}

// WithMeter returns a context whose DynamoDB calls are added to the returned Meter.
func WithMeter(ctx context.Context) (context.Context, *Meter) {
	m := &Meter{}
	return context.WithValue(ctx, meterKey{}, m), m
}

// Units returns the read and write units used so far, and the number of calls.
func (m *Meter) Units() (read, write float64, calls int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.read, m.write, m.calls
}

func (m *Meter) add(read, write float64) {
	m.mu.Lock()
	m.read += read
	m.write += write
	m.calls++
	m.mu.Unlock()
}

// meterMiddleware asks DynamoDB to report consumed capacity (free) and adds it
// to the context's Meter.
var meterMiddleware = middleware.InitializeMiddlewareFunc("KrabberMeter",
	func(ctx context.Context, in middleware.InitializeInput, next middleware.InitializeHandler) (middleware.InitializeOutput, middleware.Metadata, error) {
		m, _ := ctx.Value(meterKey{}).(*Meter)
		if m != nil {
			askForCapacity(in.Parameters)
		}
		out, md, err := next.HandleInitialize(ctx, in)
		if m != nil && err == nil {
			read, write := consumed(out.Result)
			minRead, minWrite := billedAtLeast(in.Parameters)
			m.add(max(read, minRead), max(write, minWrite))
		}
		return out, md, err
	})

// billedAtLeast is the smallest charge AWS makes for a call: half a read unit
// per item looked up (a whole unit when strongly consistent), found or not,
// and one write unit per item written (two in a transaction).
func billedAtLeast(params any) (read, write float64) {
	perRead := func(consistent *bool) float64 {
		if consistent != nil && *consistent {
			return 1
		}
		return 0.5
	}
	switch in := params.(type) {
	case *dynamodb.GetItemInput:
		return perRead(in.ConsistentRead), 0
	case *dynamodb.QueryInput:
		return perRead(in.ConsistentRead), 0
	case *dynamodb.ScanInput:
		return perRead(in.ConsistentRead), 0
	case *dynamodb.BatchGetItemInput:
		for _, ka := range in.RequestItems {
			read += float64(len(ka.Keys)) * perRead(ka.ConsistentRead)
		}
		return read, 0
	case *dynamodb.TransactGetItemsInput:
		return 2 * float64(len(in.TransactItems)), 0
	case *dynamodb.PutItemInput, *dynamodb.UpdateItemInput, *dynamodb.DeleteItemInput:
		return 0, 1
	case *dynamodb.BatchWriteItemInput:
		for _, reqs := range in.RequestItems {
			write += float64(len(reqs))
		}
		return 0, write
	case *dynamodb.TransactWriteItemsInput:
		return 0, 2 * float64(len(in.TransactItems))
	}
	return 0, 0
}

func askForCapacity(params any) {
	total := types.ReturnConsumedCapacityTotal
	switch in := params.(type) {
	case *dynamodb.GetItemInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.QueryInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.ScanInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.BatchGetItemInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.TransactGetItemsInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.PutItemInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.UpdateItemInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.DeleteItemInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.BatchWriteItemInput:
		in.ReturnConsumedCapacity = total
	case *dynamodb.TransactWriteItemsInput:
		in.ReturnConsumedCapacity = total
	}
}

// consumed splits an operation's reported capacity into read and write units.
func consumed(result any) (read, write float64) {
	one := func(c *types.ConsumedCapacity) float64 {
		if c == nil || c.CapacityUnits == nil {
			return 0
		}
		return *c.CapacityUnits
	}
	many := func(cs []types.ConsumedCapacity) float64 {
		total := 0.0
		for i := range cs {
			total += one(&cs[i])
		}
		return total
	}
	switch out := result.(type) {
	case *dynamodb.GetItemOutput:
		return one(out.ConsumedCapacity), 0
	case *dynamodb.QueryOutput:
		return one(out.ConsumedCapacity), 0
	case *dynamodb.ScanOutput:
		return one(out.ConsumedCapacity), 0
	case *dynamodb.BatchGetItemOutput:
		return many(out.ConsumedCapacity), 0
	case *dynamodb.TransactGetItemsOutput:
		return many(out.ConsumedCapacity), 0
	case *dynamodb.PutItemOutput:
		return 0, one(out.ConsumedCapacity)
	case *dynamodb.UpdateItemOutput:
		return 0, one(out.ConsumedCapacity)
	case *dynamodb.DeleteItemOutput:
		return 0, one(out.ConsumedCapacity)
	case *dynamodb.BatchWriteItemOutput:
		return 0, many(out.ConsumedCapacity)
	case *dynamodb.TransactWriteItemsOutput:
		return 0, many(out.ConsumedCapacity)
	}
	return 0, 0
}
