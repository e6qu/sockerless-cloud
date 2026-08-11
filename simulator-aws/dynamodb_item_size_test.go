package main

import "testing"

func TestDynamoDBItemSizeUsesStoredAttributeBytes(t *testing.T) {
	item := map[string]any{
		"text":   map[string]any{"S": "é"},
		"binary": map[string]any{"B": "AQID"},
		"number": map[string]any{"N": "1.2300e+10"},
		"list": map[string]any{"L": []any{
			map[string]any{"BOOL": true},
			map[string]any{"NULL": true},
		}},
		"sets": map[string]any{"BS": []any{"AQI=", "AwQF"}},
	}

	// Attribute names: 4+6+6+4+4 = 24.
	// Values: UTF-8 string 2, raw binary 3, number 3,
	// list 3 + (1+1)*2 = 7, binary set 2+3 = 5.
	if got, want := ddbItemSizeBytes(item), 44; got != want {
		t.Fatalf("ddbItemSizeBytes() = %d, want %d", got, want)
	}
}

func TestDynamoDBItemSizeBoundary(t *testing.T) {
	item := map[string]any{
		"id":      map[string]any{"S": "ok"},
		"payload": map[string]any{"S": string(make([]byte, ddbMaxItemSizeBytes-11))},
	}
	if got := ddbItemSizeBytes(item); got != ddbMaxItemSizeBytes {
		t.Fatalf("boundary item size = %d, want %d", got, ddbMaxItemSizeBytes)
	}
	if err := ddbValidateItemSize(item); err != nil {
		t.Fatalf("exactly 400 KiB item rejected: %v", err)
	}
	item["payload"] = map[string]any{"S": string(make([]byte, ddbMaxItemSizeBytes-10))}
	if err := ddbValidateItemSize(item); err == nil {
		t.Fatal("item larger than 400 KiB was accepted")
	}
}
