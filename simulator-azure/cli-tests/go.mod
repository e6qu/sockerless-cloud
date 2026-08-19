module github.com/e6qu/sockerless-cloud/simulator-azure/cli-tests

go 1.25.0

require (
	github.com/stretchr/testify v1.12.1
	software.sslmate.com/src/go-pkcs12 v0.7.3
)

require (
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

require github.com/e6qu/sockerless-cloud/realexec v0.1.0

replace github.com/e6qu/sockerless-cloud/realexec => ../../realexec
