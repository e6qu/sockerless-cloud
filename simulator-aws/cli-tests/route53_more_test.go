package aws_cli_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// r53MoreCLIZone creates a hosted zone via the aws CLI and returns its bare id,
// registering a tolerant cleanup that deletes the zone.
func r53MoreCLIZone(t *testing.T, name string) string {
	t.Helper()
	caller := "cli-more-" + time.Now().Format("150405.000000")
	out := runCLI(t, awsCLI("route53", "create-hosted-zone",
		"--name", name,
		"--caller-reference", caller,
		"--output", "json",
	))
	var res struct {
		HostedZone struct {
			Id string `json:"Id"`
		} `json:"HostedZone"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	id := strings.TrimPrefix(res.HostedZone.Id, "/hostedzone/")
	require.NotEmpty(t, id)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("route53", "delete-hosted-zone", "--id", id))
	})
	return id
}

func TestRoute53_CLIReusableDelegationSets(t *testing.T) {
	caller := "cli-ds-" + time.Now().Format("150405.000000")
	out := runCLI(t, awsCLI("route53", "create-reusable-delegation-set",
		"--caller-reference", caller, "--output", "json"))
	var created struct {
		DelegationSet struct {
			Id          string   `json:"Id"`
			NameServers []string `json:"NameServers"`
		} `json:"DelegationSet"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	id := strings.TrimPrefix(created.DelegationSet.Id, "/delegationset/")
	require.NotEmpty(t, id)
	require.Len(t, created.DelegationSet.NameServers, 4)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("route53", "delete-reusable-delegation-set", "--id", id))
	})

	getOut := runCLI(t, awsCLI("route53", "get-reusable-delegation-set", "--id", id, "--output", "json"))
	require.Contains(t, getOut, caller)

	listOut := runCLI(t, awsCLI("route53", "list-reusable-delegation-sets", "--output", "json"))
	require.Contains(t, listOut, id)

	limitOut := runCLI(t, awsCLI("route53", "get-reusable-delegation-set-limit",
		"--delegation-set-id", id, "--type", "MAX_ZONES_BY_REUSABLE_DELEGATION_SET", "--output", "json"))
	var limit struct {
		Limit struct {
			Value int64 `json:"Value"`
		} `json:"Limit"`
	}
	require.NoError(t, json.Unmarshal([]byte(limitOut), &limit))
	require.Equal(t, int64(100), limit.Limit.Value)

	runCLI(t, awsCLI("route53", "delete-reusable-delegation-set", "--id", id))
}

func TestRoute53_CLICidrCollections(t *testing.T) {
	caller := "cli-cidr-" + time.Now().Format("150405.000000")
	name := "cli-cidr-" + time.Now().Format("150405")
	out := runCLI(t, awsCLI("route53", "create-cidr-collection",
		"--name", name, "--caller-reference", caller, "--output", "json"))
	var created struct {
		Collection struct {
			Id string `json:"Id"`
		} `json:"Collection"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &created))
	id := created.Collection.Id
	require.NotEmpty(t, id)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("route53", "change-cidr-collection", "--id", id,
			"--changes", `[{"LocationName":"eu-west","Action":"DELETE_IF_EXISTS","CidrList":["198.51.100.0/24"]}]`))
		runCLIIgnore(awsCLI("route53", "delete-cidr-collection", "--id", id))
	})

	runCLI(t, awsCLI("route53", "change-cidr-collection", "--id", id,
		"--changes", `[{"LocationName":"eu-west","Action":"PUT","CidrList":["198.51.100.0/24"]}]`))

	listOut := runCLI(t, awsCLI("route53", "list-cidr-collections", "--output", "json"))
	require.Contains(t, listOut, id)

	locOut := runCLI(t, awsCLI("route53", "list-cidr-locations", "--collection-id", id, "--output", "json"))
	require.Contains(t, locOut, "eu-west")

	blockOut := runCLI(t, awsCLI("route53", "list-cidr-blocks", "--collection-id", id, "--output", "json"))
	require.Contains(t, blockOut, "198.51.100.0/24")
}

func TestRoute53_CLIDNSSECKeySigningKeys(t *testing.T) {
	zoneID := r53MoreCLIZone(t, "cli-dnssec.local")
	kskName := "cli_ksk_" + time.Now().Format("150405")
	kmsArn := "arn:aws:kms:us-east-1:123456789012:key/cli-route53-dnssec"

	createOut := runCLI(t, awsCLI("route53", "create-key-signing-key",
		"--caller-reference", "cli-ksk-"+time.Now().Format("150405.000000"),
		"--hosted-zone-id", zoneID,
		"--key-management-service-arn", kmsArn,
		"--name", kskName,
		"--status", "ACTIVE",
		"--output", "json"))
	require.Contains(t, createOut, kskName)

	runCLI(t, awsCLI("route53", "enable-hosted-zone-dnssec", "--hosted-zone-id", zoneID))

	dnssecOut := runCLI(t, awsCLI("route53", "get-dnssec", "--hosted-zone-id", zoneID, "--output", "json"))
	var dnssec struct {
		Status struct {
			ServeSignature string `json:"ServeSignature"`
		} `json:"Status"`
		KeySigningKeys []struct {
			Name string `json:"Name"`
		} `json:"KeySigningKeys"`
	}
	require.NoError(t, json.Unmarshal([]byte(dnssecOut), &dnssec))
	require.Equal(t, "SIGNING", dnssec.Status.ServeSignature)
	require.Len(t, dnssec.KeySigningKeys, 1)

	runCLI(t, awsCLI("route53", "deactivate-key-signing-key", "--hosted-zone-id", zoneID, "--name", kskName))
	runCLI(t, awsCLI("route53", "activate-key-signing-key", "--hosted-zone-id", zoneID, "--name", kskName))
	runCLI(t, awsCLI("route53", "disable-hosted-zone-dnssec", "--hosted-zone-id", zoneID))
	runCLI(t, awsCLI("route53", "delete-key-signing-key", "--hosted-zone-id", zoneID, "--name", kskName))
}

func TestRoute53_CLITrafficPolicyInstances(t *testing.T) {
	zoneID := r53MoreCLIZone(t, "cli-tpi.local")
	doc := `{"AWSPolicyFormatVersion":"2015-10-01","RecordType":"A","Endpoints":{"e":{"Type":"value","Value":"192.0.2.1"}},"StartEndpoint":"e"}`
	tpOut := runCLI(t, awsCLI("route53", "create-traffic-policy",
		"--name", "cli-tp-"+time.Now().Format("150405"),
		"--document", doc, "--output", "json"))
	var tp struct {
		TrafficPolicy struct {
			Id      string `json:"Id"`
			Version int    `json:"Version"`
		} `json:"TrafficPolicy"`
	}
	require.NoError(t, json.Unmarshal([]byte(tpOut), &tp))
	tpID := tp.TrafficPolicy.Id
	tpVer := tp.TrafficPolicy.Version
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("route53", "delete-traffic-policy", "--id", tpID, "--version", strconv.Itoa(tpVer)))
	})

	commentOut := runCLI(t, awsCLI("route53", "update-traffic-policy-comment",
		"--id", tpID, "--traffic-policy-version", strconv.Itoa(tpVer), "--comment", "cli updated", "--output", "json"))
	require.Contains(t, commentOut, "cli updated")

	createOut := runCLI(t, awsCLI("route53", "create-traffic-policy-instance",
		"--hosted-zone-id", zoneID,
		"--name", "svc.cli-tpi.local",
		"--ttl", "300",
		"--traffic-policy-id", tpID,
		"--traffic-policy-version", strconv.Itoa(tpVer),
		"--output", "json"))
	var inst struct {
		TrafficPolicyInstance struct {
			Id string `json:"Id"`
		} `json:"TrafficPolicyInstance"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &inst))
	instID := inst.TrafficPolicyInstance.Id
	require.NotEmpty(t, instID)
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("route53", "delete-traffic-policy-instance", "--id", instID))
	})

	getOut := runCLI(t, awsCLI("route53", "get-traffic-policy-instance", "--id", instID, "--output", "json"))
	require.Contains(t, getOut, zoneID)

	runCLI(t, awsCLI("route53", "update-traffic-policy-instance",
		"--id", instID, "--ttl", "600",
		"--traffic-policy-id", tpID, "--traffic-policy-version", strconv.Itoa(tpVer), "--output", "json"))

	listOut := runCLI(t, awsCLI("route53", "list-traffic-policy-instances", "--output", "json"))
	require.Contains(t, listOut, instID)

	byZoneOut := runCLI(t, awsCLI("route53", "list-traffic-policy-instances-by-hosted-zone",
		"--hosted-zone-id", zoneID, "--output", "json"))
	require.Contains(t, byZoneOut, instID)

	byPolicyOut := runCLI(t, awsCLI("route53", "list-traffic-policy-instances-by-policy",
		"--traffic-policy-id", tpID, "--traffic-policy-version", strconv.Itoa(tpVer), "--output", "json"))
	require.Contains(t, byPolicyOut, instID)

	countOut := runCLI(t, awsCLI("route53", "get-traffic-policy-instance-count", "--output", "json"))
	require.Contains(t, countOut, "TrafficPolicyInstanceCount")
}

func TestRoute53_CLIVPCAssociationAuthorizations(t *testing.T) {
	zoneID := r53MoreCLIZone(t, "cli-vpcauthz.local")
	vpcJSON := `{"VPCRegion":"us-east-1","VPCId":"vpc-cli0123456789"}`

	createOut := runCLI(t, awsCLI("route53", "create-vpc-association-authorization",
		"--hosted-zone-id", zoneID, "--vpc", vpcJSON, "--output", "json"))
	require.Contains(t, createOut, "vpc-cli0123456789")

	listOut := runCLI(t, awsCLI("route53", "list-vpc-association-authorizations",
		"--hosted-zone-id", zoneID, "--output", "json"))
	require.Contains(t, listOut, "vpc-cli0123456789")

	runCLI(t, awsCLI("route53", "delete-vpc-association-authorization",
		"--hosted-zone-id", zoneID, "--vpc", vpcJSON))

	listOut2 := runCLI(t, awsCLI("route53", "list-vpc-association-authorizations",
		"--hosted-zone-id", zoneID, "--output", "json"))
	require.NotContains(t, listOut2, "vpc-cli0123456789")
}

func TestRoute53_CLILimitsCheckerRangesAndTags(t *testing.T) {
	zoneID := r53MoreCLIZone(t, "cli-limits.local")

	acctOut := runCLI(t, awsCLI("route53", "get-account-limit",
		"--type", "MAX_HOSTED_ZONES_BY_OWNER", "--output", "json"))
	var acct struct {
		Limit struct {
			Value int64 `json:"Value"`
		} `json:"Limit"`
	}
	require.NoError(t, json.Unmarshal([]byte(acctOut), &acct))
	require.Equal(t, int64(500), acct.Limit.Value)

	zoneLimitOut := runCLI(t, awsCLI("route53", "get-hosted-zone-limit",
		"--hosted-zone-id", zoneID, "--type", "MAX_RRSETS_BY_ZONE", "--output", "json"))
	require.Contains(t, zoneLimitOut, "MAX_RRSETS_BY_ZONE")

	rangesOut := runCLI(t, awsCLI("route53", "get-checker-ip-ranges", "--output", "json"))
	require.Contains(t, rangesOut, "CheckerIpRanges")

	// Tag a health check, then batch-read with list-tags-for-resources.
	hcOut := runCLI(t, awsCLI("route53", "create-health-check",
		"--caller-reference", "cli-hc-"+time.Now().Format("150405.000000"),
		"--health-check-config", `{"IPAddress":"192.0.2.10","Port":80,"Type":"HTTP","ResourcePath":"/","RequestInterval":30,"FailureThreshold":3}`,
		"--output", "json"))
	var hc struct {
		HealthCheck struct {
			Id string `json:"Id"`
		} `json:"HealthCheck"`
	}
	require.NoError(t, json.Unmarshal([]byte(hcOut), &hc))
	hcID := hc.HealthCheck.Id
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("route53", "delete-health-check", "--health-check-id", hcID))
	})

	failOut := runCLI(t, awsCLI("route53", "get-health-check-last-failure-reason",
		"--health-check-id", hcID, "--output", "json"))
	require.Contains(t, failOut, "HealthCheckObservations")

	runCLI(t, awsCLI("route53", "change-tags-for-resource",
		"--resource-type", "healthcheck", "--resource-id", hcID,
		"--add-tags", `Key=env,Value=cli`))

	tagsOut := runCLI(t, awsCLI("route53", "list-tags-for-resources",
		"--resource-type", "healthcheck", "--resource-ids", hcID, "--output", "json"))
	require.Contains(t, tagsOut, "env")
}

func TestRoute53_CLITestDNSAnswerAndZoneUpdates(t *testing.T) {
	zoneID := r53MoreCLIZone(t, "cli-dnsanswer.local")

	changeJSON := `{"Changes":[{"Action":"CREATE","ResourceRecordSet":{"Name":"www.cli-dnsanswer.local.","Type":"A","TTL":300,"ResourceRecords":[{"Value":"192.0.2.55"}]}}]}`
	runCLI(t, awsCLI("route53", "change-resource-record-sets",
		"--hosted-zone-id", zoneID, "--change-batch", changeJSON))
	t.Cleanup(func() {
		delJSON := `{"Changes":[{"Action":"DELETE","ResourceRecordSet":{"Name":"www.cli-dnsanswer.local.","Type":"A","TTL":300,"ResourceRecords":[{"Value":"192.0.2.55"}]}}]}`
		runCLIIgnore(awsCLI("route53", "change-resource-record-sets", "--hosted-zone-id", zoneID, "--change-batch", delJSON))
	})

	answerOut := runCLI(t, awsCLI("route53", "test-dns-answer",
		"--hosted-zone-id", zoneID,
		"--record-name", "www.cli-dnsanswer.local",
		"--record-type", "A", "--output", "json"))
	var answer struct {
		ResponseCode string   `json:"ResponseCode"`
		RecordData   []string `json:"RecordData"`
	}
	require.NoError(t, json.Unmarshal([]byte(answerOut), &answer))
	require.Equal(t, "NOERROR", answer.ResponseCode)
	require.Contains(t, answer.RecordData, "192.0.2.55")

	commentOut := runCLI(t, awsCLI("route53", "update-hosted-zone-comment",
		"--id", zoneID, "--comment", "cli updated comment", "--output", "json"))
	require.Contains(t, commentOut, "cli updated comment")

	runCLI(t, awsCLI("route53", "update-hosted-zone-features",
		"--hosted-zone-id", zoneID, "--enable-accelerated-recovery"))
}
