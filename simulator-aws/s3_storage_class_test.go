package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestS3StorageClassesMatchTheVendoredModel(t *testing.T) {
	file, err := os.Open(filepath.Join("..", "specs", "cloud-api", "aws", "s3.smithy.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	var model struct {
		Shapes map[string]struct {
			Members map[string]struct {
				Traits map[string]json.RawMessage `json:"traits"`
			} `json:"members"`
		} `json:"shapes"`
	}
	if err := json.NewDecoder(reader).Decode(&model); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{}
	for _, member := range model.Shapes["com.amazonaws.s3#StorageClass"].Members {
		var value string
		if err := json.Unmarshal(member.Traits["smithy.api#enumValue"], &value); err != nil {
			t.Fatal(err)
		}
		want[value] = true
	}
	if len(want) != len(s3StorageClasses) {
		t.Fatalf("the model declares %d storage classes, the simulator %d", len(want), len(s3StorageClasses))
	}
	for class := range want {
		if !s3StorageClasses[class] {
			t.Errorf("the simulator does not accept the model's storage class %s", class)
		}
	}
}

func TestS3RestoreExpiryIsTheFollowingMidnightUTC(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 15, 0, 0, time.UTC)
	if got, want := s3RestoreExpiry(now, 2), time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("expiry = %s, want %s", got, want)
	}
	midnight := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	if got, want := s3RestoreExpiry(midnight, 1), time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Fatalf("expiry from midnight = %s, want %s", got, want)
	}
}
