package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
)

// iamServiceReferenceOperations is one vendored Service Reference document's
// actions and operations.
type iamServiceReferenceOperations struct {
	Name    string
	Actions []struct {
		Name        string
		Annotations struct {
			Properties struct{ IsTaggingOnly bool }
		}
	}
	Operations []struct {
		Name              string
		AuthorizedActions []struct{ Name, Service string }
	}
}

func loadIAMServiceReferenceOperations(t *testing.T) []iamServiceReferenceOperations {
	t.Helper()
	files, err := filepath.Glob("../specs/cloud-api/aws/service-reference/*.servicereference.json.gz")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vendored Service Reference: %v", err)
	}
	var refs []iamServiceReferenceOperations
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		var ref iamServiceReferenceOperations
		err = json.NewDecoder(zr).Decode(&ref)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		refs = append(refs, ref)
	}
	return refs
}

// iamOwnAndTaggingActions splits the actions of the operation's own service
// that the reference authorizes it as into the tagging-only ones and the rest.
func iamOwnAndTaggingActions(ref iamServiceReferenceOperations, authorized []struct{ Name, Service string }) (own, tagging []string) {
	taggingOnly := map[string]bool{}
	for _, a := range ref.Actions {
		taggingOnly[a.Name] = a.Annotations.Properties.IsTaggingOnly
	}
	seen := map[string]bool{}
	for _, a := range authorized {
		if a.Service != ref.Name || seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		if taggingOnly[a.Name] {
			tagging = append(tagging, a.Name)
		} else {
			own = append(own, a.Name)
		}
	}
	sort.Strings(own)
	sort.Strings(tagging)
	return own, tagging
}

// The generated table holds exactly the operations the vendored references
// authorize as one action other than their own name, not counting a tagging
// action the operation adds only when its request carries tags.
func TestIAMOperationActionsMatchTheVendoredReference(t *testing.T) {
	want := map[string]string{}
	for _, ref := range loadIAMServiceReferenceOperations(t) {
		declared := map[string]bool{}
		for _, a := range ref.Actions {
			declared[a.Name] = true
		}
		for _, op := range ref.Operations {
			if declared[op.Name] {
				continue
			}
			own, tagging := iamOwnAndTaggingActions(ref, op.AuthorizedActions)
			switch {
			case len(own) == 1:
				want[ref.Name+":"+op.Name] = own[0]
			case len(own) == 0 && len(tagging) == 1:
				want[ref.Name+":"+op.Name] = tagging[0]
			}
		}
	}
	if !reflect.DeepEqual(iamOperationActions, want) {
		var problems []string
		for k, v := range want {
			if iamOperationActions[k] != v {
				problems = append(problems, k+" -> "+v+" (table: "+iamOperationActions[k]+")")
			}
		}
		for k := range iamOperationActions {
			if _, ok := want[k]; !ok {
				problems = append(problems, k+": in the table, not in the reference")
			}
		}
		sort.Strings(problems)
		t.Fatalf("iamOperationActions is out of date (run scripts/gen-aws-iam-resource-types.sh):\n  %s", strings.Join(problems, "\n  "))
	}
}

// The generated table holds exactly the tagging actions each operation adds
// beside an action of its own, for every service but Amazon S3.
func TestIAMTagOnCreateActionsMatchTheVendoredReference(t *testing.T) {
	want := map[string][]string{}
	for _, ref := range loadIAMServiceReferenceOperations(t) {
		if ref.Name == "s3" {
			continue
		}
		for _, op := range ref.Operations {
			own, tagging := iamOwnAndTaggingActions(ref, op.AuthorizedActions)
			if len(own) > 0 && len(tagging) > 0 && !slices.Contains(tagging, op.Name) {
				want[ref.Name+":"+op.Name] = tagging
			}
		}
	}
	if !reflect.DeepEqual(iamTagOnCreateActions, want) {
		var problems []string
		for k, v := range want {
			if !slices.Equal(iamTagOnCreateActions[k], v) {
				problems = append(problems, fmt.Sprintf("%s -> %v (table: %v)", k, v, iamTagOnCreateActions[k]))
			}
		}
		for k := range iamTagOnCreateActions {
			if _, ok := want[k]; !ok {
				problems = append(problems, k+": in the table, not in the reference")
			}
		}
		sort.Strings(problems)
		t.Fatalf("iamTagOnCreateActions is out of date (run scripts/gen-aws-iam-resource-types.sh):\n  %s", strings.Join(problems, "\n  "))
	}
}

func targetStrings(targets []iamAuthorizationTarget) []string {
	out := make([]string, 0, len(targets))
	for _, target := range targets {
		out = append(out, target.action+" "+target.resource)
	}
	sort.Strings(out)
	return out
}

func TestOperationsAreAuthorizedAsTheirActions(t *testing.T) {
	jsonRequest := func(body string) *http.Request {
		return httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	}
	table := func(name string) string { return "arn:aws:dynamodb:us-east-1:123456789012:table/" + name }
	queryRequest := func(form url.Values) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		return r
	}
	const (
		budget      = "arn:aws:budgets::123456789012:budget/b"
		service     = "arn:aws:ecs:us-east-1:123456789012:service/c/s"
		targetGroup = "arn:aws:elasticloadbalancing:us-east-1:123456789012:targetgroup/tg/*"
		image       = "arn:aws:ec2:us-east-1::image/ami-1"

		loadBalancer   = "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/app/web/50dc6c495c0c9188"
		listener       = "arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/web/50dc6c495c0c9188/f2f7dc8efc522ab2"
		taskDefinition = "arn:aws:ecs:us-east-1:123456789012:task-definition/web:3"
	)
	for name, tc := range map[string]struct {
		r                  *http.Request
		service, operation string
		resources          []string
		want               []string
	}{
		"a batch send is a send": {
			jsonRequest(`{}`), "sqs", "SendMessageBatch", []string{"q"},
			[]string{"sqs:SendMessage q"},
		},
		"an operation that is an action stays itself": {
			jsonRequest(`{}`), "sqs", "SendMessage", []string{"q"},
			[]string{"sqs:SendMessage q"},
		},
		"each transaction item is its own write": {
			jsonRequest(`{"TransactItems":[{"Put":{"TableName":"a"}},{"Update":{"TableName":"b"}},{"ConditionCheck":{"TableName":"a"}}]}`),
			"dynamodb", "TransactWriteItems", nil,
			[]string{"dynamodb:ConditionCheckItem " + table("a"), "dynamodb:PutItem " + table("a"), "dynamodb:UpdateItem " + table("b")},
		},
		"each PartiQL statement is its own verb": {
			jsonRequest(`{"Statements":[{"Statement":"SELECT * FROM \"a\".\"by_owner\""},{"Statement":"INSERT INTO \"b\" VALUE {'id':'1'}"}]}`),
			"dynamodb", "BatchExecuteStatement", nil,
			[]string{"dynamodb:PartiQLInsert " + table("b"), "dynamodb:PartiQLSelect " + table("a") + "/index/by_owner"},
		},
		"a statement that does not parse authorizes nothing": {
			jsonRequest(`{"Statement":"FROB \"a\""}`), "dynamodb", "ExecuteStatement", nil, []string{},
		},
		"a budget with tags also tags": {
			jsonRequest(`{"ResourceTags":[{"Key":"k","Value":"v"}]}`), "budgets", "CreateBudget", []string{budget},
			[]string{"budgets:ModifyBudget " + budget, "budgets:TagResource " + budget},
		},
		"a budget without tags tags nothing": {
			jsonRequest(`{}`), "budgets", "CreateBudget", []string{budget},
			[]string{"budgets:ModifyBudget " + budget},
		},
		"a tagged service also tags the service": {
			jsonRequest(`{"serviceName":"s","tags":[{"key":"team","value":"a"}]}`), "ecs", "CreateService", []string{service},
			[]string{"ecs:CreateService " + service, "ecs:TagResource " + service},
		},
		"a tagged target group also tags the target group": {
			queryRequest(url.Values{"Action": {"CreateTargetGroup"}, "Name": {"tg"},
				"Tags.member.1.Key": {"team"}, "Tags.member.1.Value": {"a"}}),
			"elasticloadbalancing", "CreateTargetGroup", []string{targetGroup},
			[]string{"elasticloadbalancing:AddTags " + targetGroup, "elasticloadbalancing:CreateTargetGroup " + targetGroup},
		},
		"a tagged listener tags the listener under its load balancer": {
			queryRequest(url.Values{"Action": {"CreateListener"}, "LoadBalancerArn": {loadBalancer},
				"Tags.member.1.Key": {"team"}, "Tags.member.1.Value": {"a"}}),
			"elasticloadbalancing", "CreateListener", []string{loadBalancer},
			[]string{
				"elasticloadbalancing:AddTags arn:aws:elasticloadbalancing:us-east-1:123456789012:listener/app/web/50dc6c495c0c9188/*",
				"elasticloadbalancing:CreateListener " + loadBalancer,
			},
		},
		"a tagged rule tags the rule under its listener": {
			queryRequest(url.Values{"Action": {"CreateRule"}, "ListenerArn": {listener},
				"Tags.member.1.Key": {"team"}, "Tags.member.1.Value": {"a"}}),
			"elasticloadbalancing", "CreateRule", []string{listener},
			[]string{
				"elasticloadbalancing:AddTags arn:aws:elasticloadbalancing:us-east-1:123456789012:listener-rule/app/web/50dc6c495c0c9188/f2f7dc8efc522ab2/*",
				"elasticloadbalancing:CreateRule " + listener,
			},
		},
		"a tagged run tags the task in its cluster": {
			jsonRequest(`{"cluster":"arn:aws:ecs:us-east-1:123456789012:cluster/batch","taskDefinition":"web:3","tags":[{"key":"team","value":"a"}]}`),
			"ecs", "RunTask", []string{taskDefinition},
			[]string{"ecs:RunTask " + taskDefinition, "ecs:TagResource arn:aws:ecs:us-east-1:123456789012:task/batch/*"},
		},
		"a launch tags the resources its tag specifications name": {
			queryRequest(url.Values{"Action": {"RunInstances"}, "ImageId": {"ami-1"},
				"TagSpecification.1.ResourceType": {"instance"}, "TagSpecification.1.Tag.1.Key": {"team"},
				"TagSpecification.2.ResourceType": {"volume"}, "TagSpecification.2.Tag.1.Key": {"team"}}),
			"ec2", "RunInstances", []string{image},
			[]string{
				"ec2:CreateTags arn:aws:ec2:us-east-1:123456789012:instance/*",
				"ec2:CreateTags arn:aws:ec2:us-east-1:123456789012:volume/*",
				"ec2:RunInstances " + image,
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := targetStrings(iamOperationTargets(tc.r, tc.service, tc.operation, tc.resources))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("targets = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestS3OperationsAreAuthorizedAsTheirActions(t *testing.T) {
	object := "arn:aws:s3:::dest/key"
	copyRequest := httptest.NewRequest(http.MethodPut, "/dest/key", nil)
	copyRequest.Header.Set("x-amz-copy-source", "/src/some%20key?versionId=v1")
	copyRequest.Header.Set("x-amz-tagging", "a=b")
	if got, want := targetStrings(s3AuthorizationTargets(copyRequest, "CopyObject", []string{object})),
		[]string{"s3:GetObjectVersion arn:aws:s3:::src/some key", "s3:PutObject " + object, "s3:PutObjectTagging " + object}; !reflect.DeepEqual(got, want) {
		t.Errorf("CopyObject targets = %v, want %v", got, want)
	}

	list := httptest.NewRequest(http.MethodGet, "/dest?list-type=2", nil)
	if got := targetStrings(s3AuthorizationTargets(list, "ListObjectsV2", []string{"arn:aws:s3:::dest"})); !reflect.DeepEqual(got, []string{"s3:ListBucket arn:aws:s3:::dest"}) {
		t.Errorf("ListObjectsV2 targets = %v", got)
	}
	versions := httptest.NewRequest(http.MethodGet, "/dest?versions", nil)
	if got := targetStrings(s3AuthorizationTargets(versions, "ListObjectVersions", []string{"arn:aws:s3:::dest"})); !reflect.DeepEqual(got, []string{"s3:ListBucketVersions arn:aws:s3:::dest"}) {
		t.Errorf("ListObjectVersions targets = %v", got)
	}

	body := `<Delete><Object><Key>a</Key></Object><Object><Key>b</Key><VersionId>v</VersionId></Object></Delete>`
	del := httptest.NewRequest(http.MethodPost, "/dest?delete", strings.NewReader(body))
	del.SetPathValue("bucket", "dest")
	if got, want := targetStrings(s3AuthorizationTargets(del, "DeleteObjects", nil)),
		[]string{"s3:DeleteObject arn:aws:s3:::dest/a", "s3:DeleteObjectVersion arn:aws:s3:::dest/b"}; !reflect.DeepEqual(got, want) {
		t.Errorf("DeleteObjects targets = %v, want %v", got, want)
	}
	if rest, _ := io.ReadAll(del.Body); string(rest) != body {
		t.Errorf("the handler would read %q, want the whole body", rest)
	}
}
