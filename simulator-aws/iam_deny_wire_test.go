package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIAMDenyMessageKeysMatchTheVendoredModels holds the denial-message table
// to the models it describes.
//
// awsJson serialises a member under the name the model declares, and AWS's
// services disagree on whether an access-denied exception spells it `message`
// or `Message`. A client reads the name its own model declares, so a denial
// written under the other spelling reaches it with no message at all. The table
// records each service's spelling; this reads the models and fails if an entry
// disagrees with one.
func TestIAMDenyMessageKeysMatchTheVendoredModels(t *testing.T) {
	// The IAM service prefix a table entry is keyed by is not always the model
	// file's name: Amazon CloudWatch Logs authorizes as "logs", Amazon
	// EventBridge as "events", AWS Step Functions as "states".
	modelFor := map[string]string{
		"logs":       "cloudwatch-logs",
		"events":     "eventbridge",
		"states":     "sfn",
		"apigateway": "api-gateway",
	}

	checked := 0
	for service, want := range iamDenyMessageKeys {
		file := service
		if mapped, ok := modelFor[service]; ok {
			file = mapped
		}
		got, found := smithyAccessDeniedMessageMember(t, file)
		if !found {
			t.Errorf("%s: no access-denied exception in the vendored model %q — remove the entry or fix the mapping",
				service, file)
			continue
		}
		if got != want {
			t.Errorf("%s: the model declares the message member as %q and the table says %q",
				service, got, want)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("no entry was checked against a model, so this gate proved nothing")
	}
}

// smithyAccessDeniedMessageMember reports the wire name of the message member
// on a service's access-denied exception.
func smithyAccessDeniedMessageMember(t *testing.T, file string) (string, bool) {
	t.Helper()
	path := filepath.Join("..", "specs", "cloud-api", "aws", file+".smithy.json.gz")
	handle, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer func() { _ = handle.Close() }()
	reader, err := gzip.NewReader(handle)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	defer func() { _ = reader.Close() }()

	var model struct {
		Shapes map[string]struct {
			Type    string                     `json:"type"`
			Traits  map[string]json.RawMessage `json:"traits"`
			Members map[string]struct {
				Traits map[string]json.RawMessage `json:"traits"`
			} `json:"members"`
		} `json:"shapes"`
	}
	if err := json.NewDecoder(reader).Decode(&model); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}

	for id, shape := range model.Shapes {
		name := id[strings.LastIndex(id, "#")+1:]
		if shape.Type != "structure" {
			continue
		}
		if _, isError := shape.Traits["smithy.api#error"]; !isError {
			continue
		}
		if !strings.Contains(name, "AccessDenied") && !strings.Contains(name, "Unauthorized") {
			continue
		}
		for member, definition := range shape.Members {
			if !strings.EqualFold(member, "message") {
				continue
			}
			var jsonName string
			if raw, ok := definition.Traits["smithy.api#jsonName"]; ok &&
				json.Unmarshal(raw, &jsonName) == nil && jsonName != "" {
				return jsonName, true
			}
			return member, true
		}
	}
	return "", false
}
