package aws_cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestRoute53_ZoneAndRecord(t *testing.T) {
	caller := "cli-r53-" + time.Now().Format("150405.000000")
	out := runCLI(t, awsCLI("route53", "create-hosted-zone",
		"--name", "cli-route53-test.local",
		"--caller-reference", caller,
		"--output", "json",
	))
	var createResult struct {
		HostedZone struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"HostedZone"`
		ChangeInfo struct {
			Status string `json:"Status"`
		} `json:"ChangeInfo"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	zoneID := strings.TrimPrefix(createResult.HostedZone.Id, "/hostedzone/")
	require.NotEmpty(t, zoneID)
	require.Equal(t, "INSYNC", createResult.ChangeInfo.Status)

	// Add an A record + alias record via change-batch JSON
	changeJSON := `{
		"Changes": [
			{
				"Action": "CREATE",
				"ResourceRecordSet": {
					"Name": "api.cli-route53-test.local.",
					"Type": "A",
					"TTL": 300,
					"ResourceRecords": [{"Value": "203.0.113.1"}]
				}
			},
			{
				"Action": "CREATE",
				"ResourceRecordSet": {
					"Name": "cdn.cli-route53-test.local.",
					"Type": "A",
					"AliasTarget": {
						"HostedZoneId": "Z2FDTNDATAQYW2",
						"DNSName": "d111111abcdef8.cloudfront.net.",
						"EvaluateTargetHealth": false
					}
				}
			}
		]
	}`

	runCLI(t, awsCLI("route53", "change-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--change-batch", changeJSON,
		"--output", "json",
	))

	listOut := runCLI(t, awsCLI("route53", "list-resource-record-sets",
		"--hosted-zone-id", zoneID, "--output", "json"))
	var listResult struct {
		ResourceRecordSets []struct {
			Name        string `json:"Name"`
			Type        string `json:"Type"`
			AliasTarget *struct {
				HostedZoneId string `json:"HostedZoneId"`
				DNSName      string `json:"DNSName"`
			} `json:"AliasTarget,omitempty"`
		} `json:"ResourceRecordSets"`
	}
	require.NoError(t, json.Unmarshal([]byte(listOut), &listResult))
	// Initial 2 (NS+SOA) + 2 new (A + alias)
	require.GreaterOrEqual(t, len(listResult.ResourceRecordSets), 4)
	aliasFound := false
	for _, r := range listResult.ResourceRecordSets {
		if r.Name == "cdn.cli-route53-test.local." {
			require.NotNil(t, r.AliasTarget)
			require.Equal(t, "Z2FDTNDATAQYW2", r.AliasTarget.HostedZoneId)
			aliasFound = true
		}
	}
	require.True(t, aliasFound)

	// Cleanup
	delJSON := `{
		"Changes": [
			{"Action": "DELETE", "ResourceRecordSet": {"Name": "api.cli-route53-test.local.", "Type": "A", "TTL": 300, "ResourceRecords": [{"Value": "203.0.113.1"}]}},
			{"Action": "DELETE", "ResourceRecordSet": {"Name": "cdn.cli-route53-test.local.", "Type": "A", "AliasTarget": {"HostedZoneId": "Z2FDTNDATAQYW2", "DNSName": "d111111abcdef8.cloudfront.net.", "EvaluateTargetHealth": false}}}
		]
	}`
	runCLI(t, awsCLI("route53", "change-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--change-batch", delJSON,
	))
	runCLI(t, awsCLI("route53", "delete-hosted-zone", "--id", zoneID))
}

func TestRoute53_ListResourceRecordSetsSortedCursor(t *testing.T) {
	caller := "cli-r53-cursor-" + time.Now().Format("150405.000000")
	out := runCLI(t, awsCLI("route53", "create-hosted-zone",
		"--name", "cli-cursor-sort.example.com.",
		"--caller-reference", caller,
		"--output", "json",
	))
	var createResult struct {
		HostedZone struct {
			Id string `json:"Id"`
		} `json:"HostedZone"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &createResult))
	zoneID := strings.TrimPrefix(createResult.HostedZone.Id, "/hostedzone/")
	require.NotEmpty(t, zoneID)
	t.Cleanup(func() {
		delJSON := `{"Changes":[{"Action":"DELETE","ResourceRecordSet":{"Name":"api.cli-cursor-sort.example.com.","Type":"A","TTL":300,"ResourceRecords":[{"Value":"192.0.2.20"}]}}]}`
		_ = awsCLI("route53", "change-resource-record-sets",
			"--hosted-zone-id", zoneID,
			"--change-batch", delJSON,
		).Run()
		_ = awsCLI("route53", "delete-hosted-zone", "--id", zoneID).Run()
	})

	changeJSON := `{"Changes":[{"Action":"UPSERT","ResourceRecordSet":{"Name":"api.cli-cursor-sort.example.com.","Type":"A","TTL":300,"ResourceRecords":[{"Value":"192.0.2.20"}]}}]}`
	runCLI(t, awsCLI("route53", "change-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--change-batch", changeJSON,
		"--output", "json",
	))

	listOut := runCLI(t, awsCLI("route53", "list-resource-record-sets",
		"--hosted-zone-id", zoneID,
		"--start-record-name", "api.cli-cursor-sort.example.com.",
		"--start-record-type", "A",
		"--max-items", "1",
		"--output", "json",
	))
	var listResult struct {
		ResourceRecordSets []struct {
			Name string `json:"Name"`
			Type string `json:"Type"`
		} `json:"ResourceRecordSets"`
	}
	require.NoError(t, json.Unmarshal([]byte(listOut), &listResult))
	require.Len(t, listResult.ResourceRecordSets, 1)
	require.Equal(t, "api.cli-cursor-sort.example.com.", listResult.ResourceRecordSets[0].Name)
	require.Equal(t, "A", listResult.ResourceRecordSets[0].Type)
}

func TestRoute53_ListHostedZonesByName(t *testing.T) {
	suffix := strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "-")
	alphaName := "alpha.cli-r53-by-name-" + suffix + ".example.com"
	betaName := "beta.cli-r53-by-name-" + suffix + ".example.com"

	createZone := func(name string) string {
		out := runCLI(t, awsCLI("route53", "create-hosted-zone",
			"--name", name,
			"--caller-reference", "cli-r53-by-name-"+name,
			"--output", "json",
		))
		var result struct {
			HostedZone struct {
				Id string `json:"Id"`
			} `json:"HostedZone"`
		}
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		return strings.TrimPrefix(result.HostedZone.Id, "/hostedzone/")
	}

	alphaID := createZone(alphaName)
	betaID := createZone(betaName)
	defer func() {
		runCLI(t, awsCLI("route53", "delete-hosted-zone", "--id", alphaID))
		runCLI(t, awsCLI("route53", "delete-hosted-zone", "--id", betaID))
	}()

	out := runCLI(t, awsCLI("route53", "list-hosted-zones-by-name",
		"--dns-name", betaName,
		"--output", "json",
	))
	var result struct {
		HostedZones []struct {
			Id   string `json:"Id"`
			Name string `json:"Name"`
		} `json:"HostedZones"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.NotEmpty(t, result.HostedZones)
	require.Equal(t, betaName+".", result.HostedZones[0].Name)
	require.Equal(t, "/hostedzone/"+betaID, result.HostedZones[0].Id)
}

func TestRoute53_HealthCheckCLI(t *testing.T) {
	caller := "cli-hc-" + time.Now().Format("150405.000000")
	cfgJSON := `{"Type":"HTTPS","FullyQualifiedDomainName":"example.com","Port":443,"ResourcePath":"/health","RequestInterval":30,"FailureThreshold":3}`
	out := runCLI(t, awsCLI("route53", "create-health-check",
		"--caller-reference", caller,
		"--health-check-config", cfgJSON,
		"--output", "json",
	))
	var created struct {
		HealthCheck struct {
			Id                string `json:"Id"`
			HealthCheckConfig struct {
				Type                     string `json:"Type"`
				FullyQualifiedDomainName string `json:"FullyQualifiedDomainName"`
				Port                     int    `json:"Port"`
			} `json:"HealthCheckConfig"`
		} `json:"HealthCheck"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	hcID := created.HealthCheck.Id
	require.NotEmpty(t, hcID)
	require.Equal(t, "HTTPS", created.HealthCheck.HealthCheckConfig.Type)
	require.Equal(t, 443, created.HealthCheck.HealthCheckConfig.Port)
	defer func() {
		_ = awsCLI("route53", "delete-health-check", "--health-check-id", hcID).Run()
	}()

	getOut := runCLI(t, awsCLI("route53", "get-health-check", "--health-check-id", hcID, "--output", "json"))
	require.Contains(t, getOut, "example.com")

	updOut := runCLI(t, awsCLI("route53", "update-health-check",
		"--health-check-id", hcID, "--failure-threshold", "5", "--output", "json"))
	require.Contains(t, updOut, `"FailureThreshold": 5`)

	listOut := runCLI(t, awsCLI("route53", "list-health-checks", "--output", "json"))
	require.Contains(t, listOut, hcID)

	countOut := runCLI(t, awsCLI("route53", "get-health-check-count", "--output", "json"))
	require.Contains(t, countOut, "HealthCheckCount")

	statusOut := runCLI(t, awsCLI("route53", "get-health-check-status", "--health-check-id", hcID, "--output", "json"))
	require.Contains(t, statusOut, "HealthCheckObservations")
}

func TestRoute53_TrafficPolicyCLI(t *testing.T) {
	doc := `{"AWSPolicyFormatVersion":"2015-10-01","RecordType":"A","Endpoints":{"e1":{"Type":"value","Value":"203.0.113.1"}},"StartEndpoint":"e1"}`
	name := "cli-tp-" + time.Now().Format("150405.000000")
	out := runCLI(t, awsCLI("route53", "create-traffic-policy",
		"--name", name, "--document", doc, "--comment", "cli tp", "--output", "json"))
	var created struct {
		TrafficPolicy struct {
			Id      string `json:"Id"`
			Version int    `json:"Version"`
			Type    string `json:"Type"`
		} `json:"TrafficPolicy"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	tpID := created.TrafficPolicy.Id
	require.NotEmpty(t, tpID)
	require.Equal(t, 1, created.TrafficPolicy.Version)
	require.Equal(t, "A", created.TrafficPolicy.Type)
	defer func() {
		// aws CLI's global `--version` flag shadows the traffic-policy
		// `--version` param, so address the version via --cli-input-json.
		_ = awsCLI("route53", "delete-traffic-policy", "--cli-input-json", `{"Id":"`+tpID+`","Version":1}`).Run()
		_ = awsCLI("route53", "delete-traffic-policy", "--cli-input-json", `{"Id":"`+tpID+`","Version":2}`).Run()
	}()

	getOut := runCLI(t, awsCLI("route53", "get-traffic-policy", "--cli-input-json", `{"Id":"`+tpID+`","Version":1}`, "--output", "json"))
	require.Contains(t, getOut, name)

	verOut := runCLI(t, awsCLI("route53", "create-traffic-policy-version",
		"--id", tpID, "--document", doc, "--comment", "v2", "--output", "json"))
	require.Contains(t, verOut, `"Version": 2`)

	listVerOut := runCLI(t, awsCLI("route53", "list-traffic-policy-versions", "--id", tpID, "--output", "json"))
	require.Contains(t, listVerOut, tpID)

	listOut := runCLI(t, awsCLI("route53", "list-traffic-policies", "--output", "json"))
	require.Contains(t, listOut, tpID)
}

func TestRoute53_VPCQueryLoggingGeoCLI(t *testing.T) {
	caller := "cli-r53m-" + time.Now().Format("150405.000000")
	zoneOut := runCLI(t, awsCLI("route53", "create-hosted-zone",
		"--name", "cli-r53m-test.local", "--caller-reference", caller, "--output", "json"))
	var zoneResult struct {
		HostedZone struct {
			Id string `json:"Id"`
		} `json:"HostedZone"`
	}
	require.NoError(t, json.Unmarshal([]byte(zoneOut), &zoneResult))
	zoneID := strings.TrimPrefix(zoneResult.HostedZone.Id, "/hostedzone/")
	require.NotEmpty(t, zoneID)
	defer func() {
		_ = awsCLI("route53", "delete-hosted-zone", "--id", zoneID).Run()
	}()

	vpcID := "vpc-" + time.Now().Format("150405")
	assocOut := runCLI(t, awsCLI("route53", "associate-vpc-with-hosted-zone",
		"--hosted-zone-id", zoneID,
		"--vpc", "VPCRegion=us-east-1,VPCId="+vpcID,
		"--output", "json"))
	require.Contains(t, assocOut, "INSYNC")

	byVPCOut := runCLI(t, awsCLI("route53", "list-hosted-zones-by-vpc",
		"--vpc-id", vpcID, "--vpc-region", "us-east-1", "--output", "json"))
	require.Contains(t, byVPCOut, zoneID)

	disOut := runCLI(t, awsCLI("route53", "disassociate-vpc-from-hosted-zone",
		"--hosted-zone-id", zoneID,
		"--vpc", "VPCRegion=us-east-1,VPCId="+vpcID,
		"--output", "json"))
	require.Contains(t, disOut, "INSYNC")

	logArn := "arn:aws:logs:us-east-1:123456789012:log-group:/aws/route53/cli"
	qlOut := runCLI(t, awsCLI("route53", "create-query-logging-config",
		"--hosted-zone-id", zoneID,
		"--cloud-watch-logs-log-group-arn", logArn,
		"--output", "json"))
	var qlResult struct {
		QueryLoggingConfig struct {
			Id string `json:"Id"`
		} `json:"QueryLoggingConfig"`
	}
	require.NoError(t, json.Unmarshal([]byte(qlOut), &qlResult))
	qlID := qlResult.QueryLoggingConfig.Id
	require.NotEmpty(t, qlID)
	defer func() {
		_ = awsCLI("route53", "delete-query-logging-config", "--id", qlID).Run()
	}()

	qlGet := runCLI(t, awsCLI("route53", "get-query-logging-config", "--id", qlID, "--output", "json"))
	require.Contains(t, qlGet, logArn)

	qlList := runCLI(t, awsCLI("route53", "list-query-logging-configs", "--hosted-zone-id", zoneID, "--output", "json"))
	require.Contains(t, qlList, qlID)

	// Counts + geolocation.
	countOut := runCLI(t, awsCLI("route53", "get-hosted-zone-count", "--output", "json"))
	require.Contains(t, countOut, "HostedZoneCount")

	changeOut := runCLI(t, awsCLI("route53", "get-change", "--id", "C1234567890", "--output", "json"))
	require.Contains(t, changeOut, "INSYNC")

	geoOut := runCLI(t, awsCLI("route53", "get-geo-location", "--continent-code", "EU", "--output", "json"))
	require.Contains(t, geoOut, "Europe")

	listGeo := runCLI(t, awsCLI("route53", "list-geo-locations", "--output", "json"))
	require.Contains(t, listGeo, "GeoLocationDetailsList")
}
