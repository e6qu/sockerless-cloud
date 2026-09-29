package main

import (
	"encoding/json"
	"testing"
)

func mustEvent(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("event %s: %v", raw, err)
	}
	return m
}

// Every documented operator, against the same event, in each dialect that
// documents it.
func TestAWSPatternOperators(t *testing.T) {
	event := `{
		"source": "aws.ec2",
		"detail": {
			"state": "running",
			"name": "Web-Server-01",
			"file": "dir/photo.png",
			"code": 500,
			"ratio": 0.5,
			"ip": "10.1.2.3",
			"flag": true,
			"none": null,
			"empty": "",
			"tags": ["blue", "green"],
			"nested": {"deep": "value"}
		}
	}`
	cases := []struct {
		name    string
		pattern string
		want    bool
	}{
		{"exact string", `{"detail":{"state":["running"]}}`, true},
		{"exact string miss", `{"detail":{"state":["stopped"]}}`, false},
		{"exact list is OR", `{"detail":{"state":["stopped","running"]}}`, true},
		{"exact number", `{"detail":{"code":[500]}}`, true},
		{"number does not equal its string", `{"detail":{"code":["500"]}}`, false},
		{"exact bool", `{"detail":{"flag":[true]}}`, true},
		{"exact null", `{"detail":{"none":[null]}}`, true},
		{"empty string", `{"detail":{"empty":[""]}}`, true},
		{"event array matches any element", `{"detail":{"tags":["green"]}}`, true},
		{"event array miss", `{"detail":{"tags":["red"]}}`, false},
		{"prefix", `{"detail":{"state":[{"prefix":"run"}]}}`, true},
		{"prefix miss", `{"detail":{"state":[{"prefix":"stop"}]}}`, false},
		{"suffix", `{"detail":{"file":[{"suffix":".png"}]}}`, true},
		{"equals-ignore-case", `{"detail":{"name":[{"equals-ignore-case":"web-server-01"}]}}`, true},
		{"wildcard", `{"detail":{"file":[{"wildcard":"dir/*.png"}]}}`, true},
		{"wildcard spans slashes", `{"detail":{"file":[{"wildcard":"*png"}]}}`, true},
		{"wildcard miss", `{"detail":{"file":[{"wildcard":"dir/*.jpg"}]}}`, false},
		{"anything-but value", `{"detail":{"state":[{"anything-but":"stopped"}]}}`, true},
		{"anything-but excludes", `{"detail":{"state":[{"anything-but":"running"}]}}`, false},
		{"anything-but list", `{"detail":{"state":[{"anything-but":["stopped","running"]}]}}`, false},
		{"anything-but number", `{"detail":{"code":[{"anything-but":[400,404]}]}}`, true},
		{"anything-but prefix", `{"detail":{"state":[{"anything-but":{"prefix":"run"}}]}}`, false},
		{"anything-but suffix", `{"detail":{"file":[{"anything-but":{"suffix":".jpg"}}]}}`, true},
		{"anything-but needs the key", `{"detail":{"missing":[{"anything-but":"x"}]}}`, false},
		{"numeric equals", `{"detail":{"code":[{"numeric":["=",500]}]}}`, true},
		{"numeric range", `{"detail":{"code":[{"numeric":[">",100,"<=",500]}]}}`, true},
		{"numeric range miss", `{"detail":{"code":[{"numeric":[">",100,"<",500]}]}}`, false},
		{"numeric fraction", `{"detail":{"ratio":[{"numeric":[">=",0.5]}]}}`, true},
		{"numeric ignores strings", `{"detail":{"state":[{"numeric":[">",0]}]}}`, false},
		{"cidr", `{"detail":{"ip":[{"cidr":"10.0.0.0/8"}]}}`, true},
		{"cidr miss", `{"detail":{"ip":[{"cidr":"192.168.0.0/16"}]}}`, false},
		{"exists", `{"detail":{"state":[{"exists":true}]}}`, true},
		{"exists on absent", `{"detail":{"missing":[{"exists":true}]}}`, false},
		{"does not exist", `{"detail":{"missing":[{"exists":false}]}}`, true},
		{"exists is for leaves", `{"detail":{"nested":[{"exists":true}]}}`, false},
		{"nested keys", `{"detail":{"nested":{"deep":["value"]}}}`, true},
		{"sibling keys AND", `{"source":["aws.ec2"],"detail":{"state":["stopped"]}}`, false},
		{"$or", `{"detail":{"$or":[{"state":["stopped"]},{"code":[500]}]}}`, true},
		{"$or miss", `{"detail":{"$or":[{"state":["stopped"]},{"code":[404]}]}}`, false},
	}
	ev := mustEvent(t, event)
	for _, c := range cases {
		for _, d := range []struct {
			name    string
			dialect awsPatternDialect
		}{{"eventbridge", ebPatternDialect}, {"sns-body", snsBodyPolicyDialect}} {
			p, err := awsParsePattern(d.dialect, c.pattern)
			if err != nil {
				t.Errorf("%s/%s: %v", d.name, c.name, err)
				continue
			}
			if got := awsPatternMatches(p, ev); got != c.want {
				t.Errorf("%s/%s: %s → %v, want %v", d.name, c.name, c.pattern, got, c.want)
			}
		}
	}
}

// Amazon EventBridge documents operators Amazon SNS filter policies do not
// take, and Amazon SNS allows nested keys only when the policy is scoped to
// the message body.
func TestAWSPatternDialectDifferences(t *testing.T) {
	ev := mustEvent(t, `{"name":"Web-01","file":"a.png","nested":{"k":"v"}}`)
	ebOnly := []string{
		`{"name":[{"prefix":{"equals-ignore-case":"web"}}]}`,
		`{"file":[{"suffix":{"equals-ignore-case":".PNG"}}]}`,
		`{"file":[{"anything-but":{"wildcard":"*.jpg"}}]}`,
		`{"name":[{"anything-but":{"equals-ignore-case":["x","y"]}}]}`,
	}
	for _, p := range ebOnly {
		parsed, err := awsParsePattern(ebPatternDialect, p)
		if err != nil {
			t.Errorf("EventBridge rejects %s: %v", p, err)
		} else if !awsPatternMatches(parsed, ev) {
			t.Errorf("EventBridge: %s does not match", p)
		}
		if _, err := awsParsePattern(snsBodyPolicyDialect, p); err == nil {
			t.Errorf("an Amazon SNS filter policy accepts %s", p)
		}
	}
	nested := `{"nested":{"k":["v"]}}`
	if _, err := awsParsePattern(snsAttributePolicyDialect, nested); err == nil {
		t.Error("a MessageAttributes-scoped filter policy accepts nested keys")
	}
	if _, err := awsParsePattern(snsBodyPolicyDialect, nested); err != nil {
		t.Errorf("a MessageBody-scoped filter policy rejects nested keys: %v", err)
	}
}

func TestAWSPatternRejectsMalformed(t *testing.T) {
	for _, p := range []string{
		`not json`,
		`{}`,
		`{"source":"scalar"}`,
		`{"source":[]}`,
		`{"source":[["nested array"]]}`,
		`{"source":[{"prefix":1}]}`,
		`{"source":[{"prefix":"a","suffix":"b"}]}`,
		`{"source":[{"unknown":"x"}]}`,
		`{"source":[{"exists":"yes"}]}`,
		`{"n":[{"numeric":[">"]}]}`,
		`{"n":[{"numeric":["!=",5]}]}`,
		`{"n":[{"numeric":[">","5"]}]}`,
		`{"n":[{"numeric":["<",5,">",0]}]}`,
		`{"n":[{"numeric":["=",1,"<",5]}]}`,
		`{"ip":[{"cidr":"not-a-cidr"}]}`,
		`{"f":[{"wildcard":"a**b"}]}`,
		`{"f":[{"anything-but":[]}]}`,
		`{"f":[{"anything-but":{"numeric":[">",1]}}]}`,
		`{"$or":[{"a":["b"]}]}`,
		`{"$or":"x"}`,
	} {
		if _, err := awsParsePattern(ebPatternDialect, p); err == nil {
			t.Errorf("%s: accepted", p)
		}
	}
}

func TestAWSWildcardMatch(t *testing.T) {
	for _, c := range []struct {
		p, s string
		want bool
	}{
		{"*", "", true},
		{"a*", "abc", true},
		{"*c", "abc", true},
		{"a*c", "ac", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "axxcyyb", false},
		{"a?c", "abc", false},
		{"a?c", "a?c", true},
		{"[ab]", "a", false},
		{`a\*c`, "a*c", true},
		{`a\*c`, "abc", false},
		{"ab*ab", "ab", false},
	} {
		if got := awsWildcardMatch(c.p, c.s); got != c.want {
			t.Errorf("wildcard %q on %q = %v, want %v", c.p, c.s, got, c.want)
		}
	}
}

// Amazon SNS compares a Number attribute as a number and a String attribute
// as a string, matches a String.Array by any element, and does not filter on
// Binary attributes.
func TestSNSFilterPolicyOnMessageAttributes(t *testing.T) {
	attrs := map[string]SQSMessageAttribute{
		"price":  {DataType: "Number", StringValue: "300"},
		"zip":    {DataType: "String", StringValue: "300"},
		"colors": {DataType: "String.Array", StringValue: `["red","blue"]`},
		"blob":   {DataType: "Binary", BinaryValue: []byte("x")},
	}
	for _, c := range []struct {
		policy string
		want   bool
	}{
		{`{"price":[300]}`, true},
		{`{"price":[{"numeric":[">",299.5]}]}`, true},
		{`{"zip":[{"numeric":["=",300]}]}`, false},
		{`{"zip":["300"]}`, true},
		{`{"price":["300"]}`, false},
		{`{"colors":["blue"]}`, true},
		{`{"colors":[{"anything-but":"red"}]}`, true},
		{`{"blob":[{"exists":true}]}`, false},
		{`{"price":[300],"zip":["999"]}`, false},
		{`{"$or":[{"price":[1]},{"zip":["300"]}]}`, true},
	} {
		sub := SNSSubscription{Attributes: map[string]string{"FilterPolicy": c.policy}}
		if err := snsValidateSubscriptionFilter(sub.Attributes); err != nil {
			t.Errorf("%s: %v", c.policy, err)
			continue
		}
		if got := snsSubscriptionMatches(sub, "{}", attrs); got != c.want {
			t.Errorf("%s: got %v, want %v", c.policy, got, c.want)
		}
	}
}

func TestSNSFilterPolicyOnMessageBody(t *testing.T) {
	sub := SNSSubscription{Attributes: map[string]string{
		"FilterPolicyScope": "MessageBody",
		"FilterPolicy":      `{"order":{"total":[{"numeric":[">",100]}]}}`,
	}}
	if err := snsValidateSubscriptionFilter(sub.Attributes); err != nil {
		t.Fatal(err)
	}
	if !snsSubscriptionMatches(sub, `{"order":{"total":150}}`, nil) {
		t.Error("a body over the bound must match")
	}
	if snsSubscriptionMatches(sub, `{"order":{"total":50}}`, nil) {
		t.Error("a body under the bound must not match")
	}
	if snsSubscriptionMatches(sub, `not json`, nil) {
		t.Error("a body that is not JSON cannot match a MessageBody policy")
	}
}

func TestSNSSubscriptionFilterValidation(t *testing.T) {
	for _, attrs := range []map[string]string{
		{"FilterPolicy": `{"a":"b"}`},
		{"FilterPolicy": `{"a":{"b":["c"]}}`},
		{"FilterPolicy": `{"a":{"b":["c"]}}`, "FilterPolicyScope": "MessageAttributes"},
		{"FilterPolicyScope": "Everything"},
	} {
		if err := snsValidateSubscriptionFilter(attrs); err == nil {
			t.Errorf("%v: accepted", attrs)
		}
	}
	for _, attrs := range []map[string]string{
		{},
		{"FilterPolicy": "{}"},
		{"FilterPolicy": `{"a":{"b":["c"]}}`, "FilterPolicyScope": "MessageBody"},
	} {
		if err := snsValidateSubscriptionFilter(attrs); err != nil {
			t.Errorf("%v: %v", attrs, err)
		}
	}
}

// Amazon EventBridge matches a pattern against the whole event, so a rule on
// resources, account or region fires only for events that carry them.
func TestEventBridgePatternSeesWholeEvent(t *testing.T) {
	event := ebBuildEvent(EBEventRecord{
		ID: "e1", Source: "aws.ec2", DetailType: "EC2 Instance State-change Notification",
		Detail:    `{"state":"running"}`,
		Resources: []string{"arn:aws:ec2:us-east-1:000000000000:instance/i-1"},
	})
	for _, c := range []struct {
		pattern string
		want    bool
	}{
		{`{"resources":["arn:aws:ec2:us-east-1:000000000000:instance/i-1"]}`, true},
		{`{"resources":[{"prefix":"arn:aws:ec2:"}]}`, true},
		{`{"resources":["arn:aws:ec2:us-east-1:000000000000:instance/i-2"]}`, false},
		{`{"account":["` + awsAccountID() + `"],"region":["` + awsRegion() + `"]}`, true},
		{`{"detail-type":[{"wildcard":"EC2 * Notification"}]}`, true},
	} {
		if got := ebEventPatternMatches(c.pattern, event); got != c.want {
			t.Errorf("%s: got %v, want %v", c.pattern, got, c.want)
		}
	}
}
