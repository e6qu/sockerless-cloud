module github.com/e6qu/sockerless-cloud/simulator-aws/terraform-tests

go 1.25.0

require github.com/stretchr/testify v1.12.1

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
