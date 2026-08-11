package main

import "testing"

// FuzzCWInsightsFilter fuzzes the CloudWatch Logs Insights `filter` parser+evaluator.
func FuzzCWInsightsFilter(f *testing.F) {
	seeds := []string{
		"",
		"status = 200",
		"status != 200 and level = error",
		"msg like /err.*/",
		"msg like \"partial\"",
		"code in [1, 2, 3]",
		"not (a = 1 or b = 2)",
		"bareField",
		"a like /[/",
		"a like /(((((((((((((((((((((((((((((((/",
		"x in [",
		"\"unterminated",
		"/unterminated",
		"'single",
		"(((((((((((",
		"a <= ",
		"!=",
		"é = 1",
		"\xff\xfe",
		"a /regex/",
		"\\",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	rec := cwInsightsRecord{"status": "200", "level": "error", "msg": "error: boom"}
	f.Fuzz(func(t *testing.T, expr string) {
		node, err := cwParseInsightsFilter(expr)
		if err != nil {
			return
		}
		if node != nil {
			_ = node.eval(rec)
		}
	})
}
