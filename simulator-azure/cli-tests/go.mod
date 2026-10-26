module github.com/e6qu/sockerless-cloud/simulator-azure/cli-tests

go 1.26.0

require (
	github.com/stretchr/testify v1.12.1
	software.sslmate.com/src/go-pkcs12 v0.7.4
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.58.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
)

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/redis/go-redis/v9 v9.23.0
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
