package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// applyARMMergePatch applies an RFC 7396 JSON Merge Patch, the content type of
// Azure Resource Manager PATCH requests, and decodes the merge back into T, so
// fields T does not model drop exactly as on PUT.
func applyARMMergePatch[T any](resource *T, patch []byte) error {
	if !json.Valid(patch) {
		return fmt.Errorf("request body is not valid JSON")
	}
	base, err := json.Marshal(resource)
	if err != nil {
		return err
	}
	var out T
	if err := json.Unmarshal(jsonMergePatch(base, patch), &out); err != nil {
		return err
	}
	*resource = out
	return nil
}

func jsonMergePatch(target, patch json.RawMessage) json.RawMessage {
	if isJSONNull(patch) {
		return patch
	}
	var patchObj map[string]json.RawMessage
	if json.Unmarshal(patch, &patchObj) != nil {
		return patch // scalar or array — replaces the target
	}
	var targetObj map[string]json.RawMessage
	if json.Unmarshal(target, &targetObj) != nil || targetObj == nil {
		targetObj = map[string]json.RawMessage{}
	}
	for name, value := range patchObj {
		if isJSONNull(value) {
			delete(targetObj, name)
			continue
		}
		targetObj[name] = jsonMergePatch(targetObj[name], value)
	}
	merged, err := json.Marshal(targetObj)
	if err != nil {
		// Every member came from a successful Unmarshal, so re-marshaling cannot
		// fail.
		panic(err)
	}
	return merged
}

func isJSONNull(v json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}
