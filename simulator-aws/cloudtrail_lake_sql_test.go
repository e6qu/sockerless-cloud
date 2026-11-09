package main

import (
	"reflect"
	"testing"
)

func TestCloudTrailLakeSQL(t *testing.T) {
	events := []CloudTrailEvent{
		{EventId: "e1", EventName: "RunInstances", EventSource: "ec2.amazonaws.com", EventTime: "2026-01-01T00:00:01Z", Username: "alice"},
		{EventId: "e2", EventName: "DescribeVpcs", EventSource: "ec2.amazonaws.com", EventTime: "2026-01-02T00:00:00Z", ReadOnly: true, Username: "bob"},
		{EventId: "e3", EventName: "CreateBucket", EventSource: "s3.amazonaws.com", EventTime: "2026-01-03T00:00:00Z", ErrorCode: "AccessDenied", Username: "alice"},
		{EventId: "e4", EventName: "DescribeVpcs", EventSource: "ec2.amazonaws.com", EventTime: "2026-01-04T00:00:00Z", ReadOnly: true},
	}
	const store = "a1b2c3d4-5678-90ab-cdef-EXAMPLE11111"
	cases := []struct {
		name, sql string
		want      [][]map[string]string
		matched   int64
	}{
		{"projection and equality", "SELECT eventId FROM " + store + " WHERE eventName = 'DescribeVpcs'",
			[][]map[string]string{{{"eventId": "e2"}}, {{"eventId": "e4"}}}, 2},
		{"or, not and parentheses", "SELECT eventId FROM " + store + " WHERE (eventSource = 's3.amazonaws.com' OR eventName = 'RunInstances') AND NOT readOnly",
			[][]map[string]string{{{"eventId": "e1"}}, {{"eventId": "e3"}}}, 2},
		{"is null", "SELECT eventId FROM " + store + " WHERE userIdentity.userName IS NULL",
			[][]map[string]string{{{"eventId": "e4"}}}, 1},
		{"is not null and like", "SELECT eventId, errorCode FROM " + store + " WHERE errorCode IS NOT NULL AND eventName LIKE 'Create_ucket'",
			[][]map[string]string{{{"eventId": "e3"}, {"errorCode": "AccessDenied"}}}, 1},
		{"between eventTime", "SELECT eventId FROM " + store + " WHERE eventTime BETWEEN '2026-01-02 00:00:00.000' AND timestamp '2026-01-03 00:00:00'",
			[][]map[string]string{{{"eventId": "e2"}}, {{"eventId": "e3"}}}, 2},
		{"not in, order desc and limit", "SELECT eventId FROM " + store + " WHERE eventName NOT IN ('CreateBucket') ORDER BY eventTime DESC LIMIT 2",
			[][]map[string]string{{{"eventId": "e4"}}, {{"eventId": "e2"}}}, 3},
		{"group by, having and an unnamed aggregate", "SELECT userIdentity.userName, count(*) FROM " + store + " WHERE userIdentity.userName IS NOT NULL GROUP BY userIdentity.userName HAVING COUNT(*) > 1",
			[][]map[string]string{{{"userIdentity.userName": "alice"}, {"_col1": "2"}}}, 3},
		{"aggregate over no rows", "SELECT COUNT(*) AS n, MAX(eventTime) AS latest FROM " + store + " WHERE eventName = 'Nothing'",
			[][]map[string]string{{{"n": "0"}}}, 0},
		{"distinct", "SELECT DISTINCT eventSource FROM " + store + " ORDER BY eventSource",
			[][]map[string]string{{{"eventSource": "ec2.amazonaws.com"}}, {{"eventSource": "s3.amazonaws.com"}}}, 4},
		{"inequality and distinct from", "SELECT eventId FROM " + store + " WHERE eventSource <> 'ec2.amazonaws.com' OR userIdentity.userName IS DISTINCT FROM 'alice' AND eventId != 'e2'",
			[][]map[string]string{{{"eventId": "e3"}}, {{"eventId": "e4"}}}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q, err := ctSQLParse(tc.sql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if q.from != store {
				t.Fatalf("FROM: got %q", q.from)
			}
			got, matched := ctSQLRun(q, events)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rows:\n got %v\nwant %v", got, tc.want)
			}
			if matched != tc.matched {
				t.Fatalf("matched: got %d, want %d", matched, tc.matched)
			}
		})
	}
	for _, bad := range []string{
		"DELETE FROM " + store,
		"SELECT eventId",
		"SELECT eventId FROM " + store + " WHERE eventName = 'unterminated",
		"SELECT eventId FROM " + store + " LIMIT ten",
		"SELECT md5(eventId) FROM " + store,
	} {
		if _, err := ctSQLParse(bad); err == nil {
			t.Errorf("%q: want a parse error", bad)
		}
	}
}
