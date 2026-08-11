package gcp_sdk_test

import (
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
)

var protoJSONTimestampPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:Z|\.\d{3}Z|\.\d{6}Z|\.\d{9}Z)$`)

func assertProtoJSONTimestamp(t *testing.T, value string) {
	t.Helper()
	assert.Regexp(t, protoJSONTimestampPattern, value)
}
