module github.com/e6qu/sockerless-cloud/simulator-gcp/cli-tests

go 1.26.0

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/stretchr/testify v1.12.1
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
