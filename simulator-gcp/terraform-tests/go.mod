module github.com/e6qu/sockerless-cloud/simulator-gcp/terraform-tests

go 1.26.0

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/stretchr/testify v1.12.1
	golang.org/x/oauth2 v0.37.0
)

require (
	cloud.google.com/go/compute/metadata v0.10.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.46.0 // indirect
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
