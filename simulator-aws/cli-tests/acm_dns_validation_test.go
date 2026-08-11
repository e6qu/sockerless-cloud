package aws_cli_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestACM_DNSValidationIssuesCert drives the flow through the
// aws CLI: request a DNS cert with a wildcard SAN, create the _acm-challenge
// records in Route53, and confirm the cert transitions to ISSUED. Also asserts
// the wildcard SAN's record name is de-wildcarded.
func TestACM_DNSValidationIssuesCert(t *testing.T) {
	out := runCLI(t, awsCLI("acm", "request-certificate",
		"--domain-name", "app.cli.example.test",
		"--subject-alternative-names", "*.devbox.cli.example.test",
		"--validation-method", "DNS",
		"--output", "json",
	))
	var created struct{ CertificateArn string }
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	arn := created.CertificateArn
	require.NotEmpty(t, arn)

	descOut := runCLI(t, awsCLI("acm", "describe-certificate", "--certificate-arn", arn, "--output", "json"))
	var desc struct {
		Certificate struct {
			Status                  string
			DomainValidationOptions []struct {
				DomainName     string
				ResourceRecord *struct{ Name, Type, Value string }
			}
		}
	}
	require.NoError(t, json.Unmarshal([]byte(descOut), &desc))
	require.Equal(t, "PENDING_VALIDATION", desc.Certificate.Status)

	type rr struct{ name, value string }
	var recs []rr
	for _, dvo := range desc.Certificate.DomainValidationOptions {
		require.NotNil(t, dvo.ResourceRecord, "DNS DVO must carry a ResourceRecord")
		if dvo.DomainName == "*.devbox.cli.example.test" {
			require.Equal(t, "_acm-challenge.devbox.cli.example.test.", dvo.ResourceRecord.Name)
			require.NotContains(t, dvo.ResourceRecord.Name, "*")
		}
		recs = append(recs, rr{dvo.ResourceRecord.Name, dvo.ResourceRecord.Value})
	}

	zoneOut := runCLI(t, awsCLI("route53", "create-hosted-zone",
		"--name", "cli.example.test", "--caller-reference", "acm-cli-"+arn[len(arn)-8:], "--output", "json"))
	var zone struct{ HostedZone struct{ Id string } }
	require.NoError(t, json.Unmarshal([]byte(zoneOut), &zone))
	zid := strings.TrimPrefix(zone.HostedZone.Id, "/hostedzone/")

	type record struct {
		Value string `json:"Value"`
	}
	type rrset struct {
		Name            string   `json:"Name"`
		Type            string   `json:"Type"`
		TTL             int      `json:"TTL"`
		ResourceRecords []record `json:"ResourceRecords"`
	}
	type change struct {
		Action            string `json:"Action"`
		ResourceRecordSet rrset  `json:"ResourceRecordSet"`
	}
	var batch struct {
		Changes []change `json:"Changes"`
	}
	for _, r := range recs {
		batch.Changes = append(batch.Changes, change{
			Action:            "UPSERT",
			ResourceRecordSet: rrset{Name: r.name, Type: "CNAME", TTL: 60, ResourceRecords: []record{{Value: r.value}}},
		})
	}
	batchJSON, err := json.Marshal(batch)
	require.NoError(t, err)
	runCLI(t, awsCLI("route53", "change-resource-record-sets",
		"--hosted-zone-id", zid, "--change-batch", string(batchJSON), "--output", "json"))

	descOut2 := runCLI(t, awsCLI("acm", "describe-certificate", "--certificate-arn", arn, "--output", "json"))
	var desc2 struct {
		Certificate struct{ Status string }
	}
	require.NoError(t, json.Unmarshal([]byte(descOut2), &desc2))
	require.Equal(t, "ISSUED", desc2.Certificate.Status)
}
