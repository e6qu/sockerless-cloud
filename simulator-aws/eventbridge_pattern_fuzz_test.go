package main

import "testing"

// FuzzEBEventPatternMatches fuzzes the EventBridge event-pattern matcher with a
// hostile pattern JSON and a hostile event detail. The matcher walks
// attacker-supplied nested JSON (objects, arrays, content-filter objects with
// prefix/suffix/numeric/cidr/anything-but terms) performing many type
// assertions on decoded values; none may panic, hang, or OOM on malformed input.
func FuzzEBEventPatternMatches(f *testing.F) {
	patterns := []string{
		``,
		`{}`,
		`{"source":["aws.ec2"]}`,
		`{"detail":{"state":["running","stopped"]}}`,
		`{"detail":{"x":[{"prefix":"abc"}]}}`,
		`{"detail":{"x":[{"suffix":"z"}]}}`,
		`{"detail":{"x":[{"equals-ignore-case":"ABC"}]}}`,
		`{"detail":{"n":[{"numeric":[">",0,"<=",100]}]}}`,
		`{"detail":{"n":[{"numeric":[">"]}]}}`,
		`{"detail":{"ip":[{"cidr":"10.0.0.0/8"}]}}`,
		`{"detail":{"x":[{"anything-but":["a","b"]}]}}`,
		`{"detail":{"x":[{"exists":true}]}}`,
		`{"detail":{"x":[{"exists":"notabool"}]}}`,
		`{"detail":{"n":[{"numeric":["=",null]}]}}`,
		`{"detail":{"x":[{"prefix":123}]}}`,
		`{"detail":{"x":[[[[[[]]]]]]}}`,
		`{"detail":{"x":{"y":{"z":["v"]}}}}`,
		`{"detail":[{"a":1}]}`,
		`not-json`,
		`{"source":"not-an-array"}`,
	}
	details := []string{
		``,
		`{}`,
		`{"state":"running"}`,
		`{"x":"abcdef"}`,
		`{"n":42}`,
		`{"ip":"10.1.2.3"}`,
		`{"x":["a","b","c"]}`,
		`{"x":{"y":{"z":"v"}}}`,
		`null`,
		`not-json`,
		`{"n":"not-a-number"}`,
	}
	for _, p := range patterns {
		for _, d := range details {
			f.Add(p, "aws.ec2", "EC2 State Change", d)
		}
	}
	f.Fuzz(func(t *testing.T, pattern, source, detailType, detail string) {
		_ = ebEventPatternMatches(pattern, source, detailType, detail)
	})
}
