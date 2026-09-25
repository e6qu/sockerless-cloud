package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestAWSErrorMessageMembersMatchTheVendoredModels holds the generated table
// to the models it was generated from: a re-vendored model that renames an
// error's message member, or adds an error, fails here until
// scripts/gen-aws-error-message-members.go is run again.
func TestAWSErrorMessageMembersMatchTheVendoredModels(t *testing.T) {
	models, err := filepath.Glob(filepath.Join("..", "specs", "cloud-api", "aws", "*.smithy.json.gz"))
	if err != nil || len(models) == 0 {
		t.Fatalf("no vendored models: %v", err)
	}
	want := map[string]map[string]string{}
	for _, path := range models {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := gzip.NewReader(file)
		if err != nil {
			t.Fatal(err)
		}
		var model struct {
			Shapes map[string]struct {
				Members map[string]json.RawMessage `json:"members"`
				Traits  map[string]json.RawMessage `json:"traits"`
			} `json:"shapes"`
		}
		err = json.NewDecoder(reader).Decode(&model)
		_ = file.Close()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		members := map[string]string{}
		for id, shape := range model.Shapes {
			if _, isError := shape.Traits["smithy.api#error"]; !isError {
				continue
			}
			for member := range shape.Members {
				if strings.EqualFold(member, "message") {
					members[id[strings.LastIndex(id, "#")+1:]] = member
				}
			}
		}
		if len(members) > 0 {
			want[strings.TrimSuffix(filepath.Base(path), ".smithy.json.gz")] = members
		}
	}
	if !reflect.DeepEqual(awsErrorMessageMembers, want) {
		t.Fatal("aws_error_members_gen.go is out of date with the vendored models: run go run scripts/gen-aws-error-message-members.go from the repository root")
	}
}

// TestAWSErrorBodySpellsTheMessageAsTheModelDoes checks the three cases: a
// declared error takes its own member, an undeclared one takes a uniform
// model's spelling, and only in a mixed model does an undeclared error carry
// both.
func TestAWSErrorBodySpellsTheMessageAsTheModelDoes(t *testing.T) {
	for _, c := range []struct {
		model, code string
		want        []string
	}{
		{"glue", "EntityNotFoundException", []string{"Message"}},
		{"glue", "NotAnErrorGlueDeclares", []string{"Message"}},
		{"acm", "AccessDeniedException", []string{awsErrorMessageMembers["acm"]["AccessDeniedException"]}},
		{"acm", "NotAnErrorAcmDeclares", []string{"Message", "message"}},
	} {
		body := awsErrorBody(c.model, c.code, "text")
		var got []string
		for key := range body {
			if key != "__type" {
				got = append(got, key)
			}
		}
		if len(got) != len(c.want) {
			t.Errorf("%s %s: message under %v, want %v", c.model, c.code, got, c.want)
			continue
		}
		for _, key := range c.want {
			if body[key] != "text" {
				t.Errorf("%s %s: no message under %q: %v", c.model, c.code, key, body)
			}
		}
	}
}
