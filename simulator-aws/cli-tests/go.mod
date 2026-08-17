module github.com/e6qu/sockerless-cloud/simulator-aws/cli-tests

go 1.25.0

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/stretchr/testify v1.12.0
	golang.org/x/crypto v0.55.0
)

require (
	github.com/kr/pretty v0.3.1 // indirect
	github.com/rogpeppe/go-internal v1.14.1 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
