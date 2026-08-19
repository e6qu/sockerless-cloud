module github.com/e6qu/sockerless-cloud/simulator-azure/terraform-tests

go 1.25.0

require (
	github.com/e6qu/sockerless-cloud/realexec v0.1.0
	github.com/stretchr/testify v1.12.1
)

require (
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

replace github.com/e6qu/sockerless-cloud/realexec => ../../realexec
