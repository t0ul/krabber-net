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

// ReportReason is one choice on the report form.
type ReportReason struct{ Key, Label string }

// ReportReasons are the choices on the report form, in display order.
var ReportReasons = []ReportReason{
	{"spam", "Spam or scam"},
	{"harassment", "Harassment or bullying"},
	{"hate", "Hate or threats of violence"},
	{"sexual", "Sexual content"},
	{"other", "Something else"},
}

// ValidReportReason reports whether key is one of ReportReasons.
func ValidReportReason(key string) bool {
	for _, r := range ReportReasons {
		if r.Key == key {
			return true
		}
	}
	return false
}

// ReportLabel is the label of a report reason.
func ReportLabel(key string) string {
	for _, r := range ReportReasons {
		if r.Key == key {
			return r.Label
		}
	}
	return key
}

// MaxReportNote is the longest note a reporter can add, in characters.
const MaxReportNote = 280

// Report is a molt's entry in the report queue. It's open (on GSI8) while
// reports wait for a moderator; a new report after a decision reopens it.
type Report struct {
	PK     string `dynamodbav:"PK"`
	SK     string `dynamodbav:"SK"`
	GSI8PK string `dynamodbav:"GSI8PK,omitempty"`
	GSI8SK string `dynamodbav:"GSI8SK,omitempty"`

	MoltID      string    `dynamodbav:"molt_id"`
	AuthorID    string    `dynamodbav:"author_id"`
	Author      string    `dynamodbav:"author"`
	Snippet     string    `dynamodbav:"snippet"`
	OpenReports int       `dynamodbav:"open_reports"`
	Reasons     []string  `dynamodbav:"reasons,stringset,omitempty"`
	LastAt      time.Time `dynamodbav:"last_at"`
	Resolution  string    `dynamodbav:"resolution,omitempty"` // "removed", "dismissed" or "deleted" (by the author)
	ResolvedBy  string    `dynamodbav:"resolved_by,omitempty"`
}

// ReportRow is one crab's report of a molt. Each crab reports a molt once.
type ReportRow struct {
	PK         string    `dynamodbav:"PK"`
	SK         string    `dynamodbav:"SK"`
	ReporterID string    `dynamodbav:"reporter_id"`
	Reporter   string    `dynamodbav:"reporter"`
	Reason     string    `dynamodbav:"reason"`
	Note       string    `dynamodbav:"note,omitempty"`
	CreatedAt  time.Time `dynamodbav:"created_at"`
}

// ReportMolt records a report and puts the molt in the queue. Reporting the
// same molt twice returns ErrAlreadyExists.
func (s *Store) ReportMolt(ctx context.Context, by *Crab, m *Molt, reason, note string) error {
	if m.AuthorID == by.ID {
		return ErrNotAllowed
	}
	now := s.now()
	row, err := marshal(ReportRow{
		PK:         reportPK(m.ID),
		SK:         reportRowSK(by.ID),
		ReporterID: by.ID,
		Reporter:   by.UserName,
		Reason:     reason,
		Note:       note,
		CreatedAt:  now,
	})
	if err != nil {
		return err
	}
	err = s.transact(ctx,
		s.putNew(row),
		types.TransactWriteItem{Update: &types.Update{
			TableName: s.tableName(),
			Key:       keyOf(reportPK(m.ID), reportSummarySK()),
			UpdateExpression: aws.String("SET GSI8PK = :q, GSI8SK = if_not_exists(GSI8SK, :sk), molt_id = :id, " +
				"author_id = :aid, author = :a, snippet = :snip, last_at = :now " +
				"ADD open_reports :one, reasons :r REMOVE resolution, resolved_by"),
			ExpressionAttributeValues: map[string]types.AttributeValue{
				":q":    str(queueReports),
				":sk":   str(newID()),
				":id":   str(m.ID),
				":aid":  str(m.AuthorID),
				":a":    str(m.Author),
				":snip": str(Snippet(m.Content)),
				":now":  str(now.Format(time.RFC3339Nano)), // how attributevalue stores time.Time
				":one":  num(1),
				":r":    &types.AttributeValueMemberSS{Value: []string{reason}},
			},
		}},
	)
	switch {
	case cancelledAt(err, 0):
		return ErrAlreadyExists
	case err != nil:
		return fmt.Errorf("report molt: %w", err)
	}
	return nil
}

// OpenReports returns molts waiting for a moderator, oldest first.
func (s *Store) OpenReports(ctx context.Context, limit int) ([]Report, error) {
	reports, err := queryAll[Report](ctx, s.db, &dynamodb.QueryInput{
		TableName:                 s.tableName(),
		IndexName:                 aws.String(gsiWorkQueue),
		KeyConditionExpression:    aws.String("GSI8PK = :q"),
		ExpressionAttributeValues: map[string]types.AttributeValue{":q": str(queueReports)},
		Limit:                     pageLimit(limit),
	}, limit)
	if err != nil {
		return nil, fmt.Errorf("open reports: %w", err)
	}
	return reports, nil
}

// ReportsOn returns a molt's queue entry (nil if it was never reported) and
// every report of it.
func (s *Store) ReportsOn(ctx context.Context, moltID string) (*Report, []ReportRow, error) {
	var summary Report
	switch err := s.getItem(ctx, reportPK(moltID), reportSummarySK(), &summary); {
	case errors.Is(err, ErrNotFound):
		return nil, nil, nil
	case err != nil:
		return nil, nil, fmt.Errorf("reports on molt: %w", err)
	}
	rows, err := queryAll[ReportRow](ctx, s.db, &dynamodb.QueryInput{
		TableName:              s.tableName(),
		KeyConditionExpression: aws.String("PK = :pk AND begins_with(SK, :by)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":pk": str(reportPK(moltID)),
			":by": str(reportRowSK("")),
		},
	}, 0)
	if err != nil {
		return nil, nil, fmt.Errorf("reports on molt: %w", err)
	}
	return &summary, rows, nil
}

// ResolveReports takes a molt off the queue. Doing so for a molt that was
// never reported is a no-op.
func (s *Store) ResolveReports(ctx context.Context, moltID, moderator, resolution string) error {
	_, err := s.db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
		TableName:           s.tableName(),
		Key:                 keyOf(reportPK(moltID), reportSummarySK()),
		UpdateExpression:    aws.String("SET open_reports = :z, resolution = :res, resolved_by = :by REMOVE GSI8PK, GSI8SK, reasons"),
		ConditionExpression: aws.String("attribute_exists(PK)"),
		ExpressionAttributeValues: map[string]types.AttributeValue{
			":z":   num(0),
			":res": str(resolution),
			":by":  str(moderator),
		},
	})
	if err != nil && !conditionFailed(err) {
		return fmt.Errorf("resolve reports: %w", err)
	}
	return nil
}
