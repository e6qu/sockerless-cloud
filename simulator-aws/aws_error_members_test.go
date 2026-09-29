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

// TestAWSErrorMessageMembersMatchTheVendoredModels holds the generated tables
// to the models they were generated from: a re-vendored model that renames an
// error's message member, adds an error, or renames its service or signing
// name fails here until scripts/gen-aws-error-message-members.go is run again.
func TestAWSErrorMessageMembersMatchTheVendoredModels(t *testing.T) {
	models, err := filepath.Glob(filepath.Join("..", "specs", "cloud-api", "aws", "*.smithy.json.gz"))
	if err != nil || len(models) == 0 {
		t.Fatalf("no vendored models: %v", err)
	}
	want := map[string]map[string]string{}
	wantByShape := map[string]string{}
	wantBySigning := map[string][]string{}
	wantCodes := map[string]map[string]bool{}
	for _, path := range models {
		modelName := strings.TrimSuffix(filepath.Base(path), ".smithy.json.gz")
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
				Type    string `json:"type"`
				Members map[string]struct {
					Traits map[string]json.RawMessage `json:"traits"`
				} `json:"members"`
				Traits map[string]json.RawMessage `json:"traits"`
			} `json:"shapes"`
		}
		err = json.NewDecoder(reader).Decode(&model)
		_ = file.Close()
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		restJSON := false
		for id, shape := range model.Shapes {
			if shape.Type != "service" {
				continue
			}
			wantByShape[id[strings.LastIndex(id, "#")+1:]] = modelName
			_, restJSON = shape.Traits["aws.protocols#restJson1"]
			var sigv4 struct {
				Name string `json:"name"`
			}
			if raw, ok := shape.Traits["aws.auth#sigv4"]; ok && json.Unmarshal(raw, &sigv4) == nil && sigv4.Name != "" {
				wantBySigning[sigv4.Name] = append(wantBySigning[sigv4.Name], modelName)
			}
		}
		members := map[string]string{}
		for id, shape := range model.Shapes {
			if _, isError := shape.Traits["smithy.api#error"]; !isError {
				continue
			}
			if _, ok := shape.Members["ErrorCode"]; ok {
				if wantCodes[modelName] == nil {
					wantCodes[modelName] = map[string]bool{}
				}
				wantCodes[modelName][id[strings.LastIndex(id, "#")+1:]] = true
			}
			for member, definition := range shape.Members {
				if !strings.EqualFold(member, "message") {
					continue
				}
				wire := member
				if raw, ok := definition.Traits["smithy.api#jsonName"]; ok && restJSON {
					if err := json.Unmarshal(raw, &wire); err != nil {
						t.Fatalf("%s %s jsonName: %v", path, id, err)
					}
				}
				members[id[strings.LastIndex(id, "#")+1:]] = wire
			}
		}
		if len(members) > 0 {
			want[modelName] = members
		}
	}
	const regenerate = "aws_error_members_gen.go is out of date with the vendored models: run go run scripts/gen-aws-error-message-members.go from the repository root"
	if !reflect.DeepEqual(awsErrorMessageMembers, want) {
		t.Fatal(regenerate)
	}
	if !reflect.DeepEqual(awsModelByServiceShape, wantByShape) {
		t.Fatal(regenerate)
	}
	if !reflect.DeepEqual(awsModelsBySigningName, wantBySigning) {
		t.Fatal(regenerate)
	}
	if !reflect.DeepEqual(awsErrorCodeMembers, wantCodes) {
		t.Fatal(regenerate)
	}
	// restJson1 writes a member under its jsonName: Amazon API Gateway V2
	// models its errors' Message member with the jsonName "message".
	if got := awsErrorMessageMembers["apigatewayv2"]["NotFoundException"]; got != "message" {
		t.Fatalf("apigatewayv2 NotFoundException message member = %q, want its jsonName %q", got, "message")
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
		{"efs", "FileSystemNotFound", []string{"ErrorCode", "Message"}},
		{"efs", "NotAnErrorEFSDeclares", []string{"ErrorCode", "Message"}},
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
			want := "text"
			if key == "ErrorCode" {
				want = c.code
			}
			if body[key] != want {
				t.Errorf("%s %s: %q = %q, want %q", c.model, c.code, key, body[key], want)
			}
		}
	}
}
