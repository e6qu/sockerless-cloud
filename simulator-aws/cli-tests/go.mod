module github.com/e6qu/sockerless-cloud/simulator-aws/cli-tests

go 1.26.0

require (
	github.com/e6qu/sockerless-cloud/testutil v0.1.0
	github.com/go-sql-driver/mysql v1.10.1
	github.com/jackc/pgx/v5 v5.11.0
	github.com/stretchr/testify v1.12.1
	golang.org/x/crypto v0.58.0
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/beevik/etree v1.8.1 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jonboulle/clockwork v0.5.0 // indirect
	github.com/russellhaering/goxmldsig v1.6.1 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/e6qu/sockerless-cloud/testutil => ../../testutil
