package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// applyARMMergePatch applies an RFC 7396 JSON Merge Patch — the content type
// ARM PATCH requests carry — to a typed resource: patch members replace or
// recurse into the stored document, and an explicit null removes the member.
// The merged document is decoded back into the same type, so fields the type
// does not model are dropped exactly as the PUT path drops them.
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

// jsonMergePatch implements the RFC 7396 merge algorithm over two valid JSON
// documents: an object patch merges member-wise (null removes the member,
// nested objects recurse); any non-object patch replaces the target wholesale.
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
		// Unreachable: every member is a json.RawMessage produced by a
		// successful Unmarshal, so re-marshaling cannot fail.
		panic(err)
	}
	return merged
}

func isJSONNull(v json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}
