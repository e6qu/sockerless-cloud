module github.com/e6qu/sockerless-cloud/simulator-aws/cli-tests

go 1.26.0

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/stretchr/testify v1.12.1
	golang.org/x/crypto v0.57.0
)

require (
	github.com/beevik/etree v1.8.1 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/russellhaering/goxmldsig v1.6.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
