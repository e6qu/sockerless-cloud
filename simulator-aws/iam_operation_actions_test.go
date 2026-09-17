package main

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// The generated table holds exactly the operations the vendored references
// authorize as one action other than their own name.
func TestIAMOperationActionsMatchTheVendoredReference(t *testing.T) {
	files, err := filepath.Glob("../specs/cloud-api/aws/service-reference/*.servicereference.json.gz")
	if err != nil || len(files) == 0 {
		t.Fatalf("no vendored Service Reference: %v", err)
	}
	want := map[string]string{}
	for _, file := range files {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		zr, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		var ref struct {
			Name       string
			Actions    []struct{ Name string }
			Operations []struct {
				Name              string
				AuthorizedActions []struct{ Name, Service string }
			}
		}
		err = json.NewDecoder(zr).Decode(&ref)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		declared := map[string]bool{}
		for _, a := range ref.Actions {
			declared[a.Name] = true
		}
		for _, op := range ref.Operations {
			if declared[op.Name] {
				continue
			}
			actions := map[string]bool{}
			for _, a := range op.AuthorizedActions {
				if a.Service == ref.Name {
					actions[a.Name] = true
				}
			}
			if len(actions) == 1 {
				for action := range actions {
					want[ref.Name+":"+op.Name] = action
				}
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
			jsonRequest(`{"ResourceTags":[{"Key":"k","Value":"v"}]}`), "budgets", "CreateBudget", []string{"b"},
			[]string{"budgets:ModifyBudget b", "budgets:TagResource b"},
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
