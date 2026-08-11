package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsAccountAndQueryPolicyCLI exercises account policies, query
// definitions, and resource policies through the aws CLI.
func TestLogsAccountAndQueryPolicyCLI(t *testing.T) {
	// Account policy.
	policyName := "cli-account-dp-policy"
	doc := `{"Rules":[{"DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Deidentify":{"MaskConfig":{}}}}]}`
	defer runCLIIgnore(awsCLI("logs", "delete-account-policy",
		"--policy-name", policyName, "--policy-type", "DATA_PROTECTION_POLICY"))

	runCLI(t, awsCLI("logs", "put-account-policy",
		"--policy-name", policyName,
		"--policy-type", "DATA_PROTECTION_POLICY",
		"--policy-document", doc,
		"--scope", "ALL"))

	out := runCLI(t, awsCLI("logs", "describe-account-policies",
		"--policy-type", "DATA_PROTECTION_POLICY", "--output", "json"))
	var ap struct {
		AccountPolicies []struct {
			PolicyName string `json:"policyName"`
			PolicyType string `json:"policyType"`
		} `json:"accountPolicies"`
	}
	parseJSON(t, out, &ap)
	foundAP := false
	for _, p := range ap.AccountPolicies {
		if p.PolicyName == policyName {
			foundAP = true
			assert.Equal(t, "DATA_PROTECTION_POLICY", p.PolicyType)
		}
	}
	assert.True(t, foundAP, "account policy should be listed")
	runCLI(t, awsCLI("logs", "delete-account-policy",
		"--policy-name", policyName, "--policy-type", "DATA_PROTECTION_POLICY"))

	// Query definition.
	qOut := runCLI(t, awsCLI("logs", "put-query-definition",
		"--name", "cli-query-def",
		"--query-string", "fields @timestamp, @message", "--output", "json"))
	var qd struct {
		QueryDefinitionId string `json:"queryDefinitionId"`
	}
	parseJSON(t, qOut, &qd)
	require.NotEmpty(t, qd.QueryDefinitionId)
	defer runCLIIgnore(awsCLI("logs", "delete-query-definition",
		"--query-definition-id", qd.QueryDefinitionId))

	descQ := runCLI(t, awsCLI("logs", "describe-query-definitions",
		"--query-definition-name-prefix", "cli-query", "--output", "json"))
	var qdl struct {
		QueryDefinitions []struct {
			QueryDefinitionId string `json:"queryDefinitionId"`
			Name              string `json:"name"`
		} `json:"queryDefinitions"`
	}
	parseJSON(t, descQ, &qdl)
	foundQ := false
	for _, d := range qdl.QueryDefinitions {
		if d.QueryDefinitionId == qd.QueryDefinitionId {
			foundQ = true
		}
	}
	assert.True(t, foundQ, "query definition should be listed")
	runCLI(t, awsCLI("logs", "delete-query-definition", "--query-definition-id", qd.QueryDefinitionId))

	// Resource policy.
	rpName := "cli-resource-policy"
	rpDoc := `{"Version":"2012-10-17","Statement":[{"Sid":"R53","Effect":"Allow","Principal":{"Service":"route53.amazonaws.com"},"Action":"logs:PutLogEvents","Resource":"*"}]}`
	defer runCLIIgnore(awsCLI("logs", "delete-resource-policy", "--policy-name", rpName))
	runCLI(t, awsCLI("logs", "put-resource-policy",
		"--policy-name", rpName, "--policy-document", rpDoc))
	rpOut := runCLI(t, awsCLI("logs", "describe-resource-policies", "--output", "json"))
	var rpl struct {
		ResourcePolicies []struct {
			PolicyName string `json:"policyName"`
		} `json:"resourcePolicies"`
	}
	parseJSON(t, rpOut, &rpl)
	foundRP := false
	for _, p := range rpl.ResourcePolicies {
		if p.PolicyName == rpName {
			foundRP = true
		}
	}
	assert.True(t, foundRP, "resource policy should be listed")
	runCLI(t, awsCLI("logs", "delete-resource-policy", "--policy-name", rpName))
}

// TestLogsDestinationCLI exercises cross-account destinations.
func TestLogsDestinationCLI(t *testing.T) {
	name := "cli-destination"
	defer runCLIIgnore(awsCLI("logs", "delete-destination", "--destination-name", name))

	out := runCLI(t, awsCLI("logs", "put-destination",
		"--destination-name", name,
		"--target-arn", "arn:aws:kinesis:us-east-1:123456789012:stream/logs",
		"--role-arn", "arn:aws:iam::123456789012:role/CWLtoKinesisRole",
		"--output", "json"))
	var put struct {
		Destination struct {
			DestinationName string `json:"destinationName"`
			Arn             string `json:"arn"`
		} `json:"destination"`
	}
	parseJSON(t, out, &put)
	assert.Equal(t, name, put.Destination.DestinationName)
	require.NotEmpty(t, put.Destination.Arn)

	runCLI(t, awsCLI("logs", "put-destination-policy",
		"--destination-name", name,
		"--access-policy", `{"Version":"2012-10-17","Statement":[]}`))

	desc := runCLI(t, awsCLI("logs", "describe-destinations",
		"--destination-name-prefix", "cli-dest", "--output", "json"))
	var dl struct {
		Destinations []struct {
			DestinationName string `json:"destinationName"`
			AccessPolicy    string `json:"accessPolicy"`
		} `json:"destinations"`
	}
	parseJSON(t, desc, &dl)
	found := false
	for _, d := range dl.Destinations {
		if d.DestinationName == name {
			found = true
			assert.NotEmpty(t, d.AccessPolicy)
		}
	}
	assert.True(t, found, "destination should be listed")
	runCLI(t, awsCLI("logs", "delete-destination", "--destination-name", name))
}

// TestLogsDeliveryCLI exercises the vended-log delivery surface: delivery
// sources, delivery destinations (+ policy), and a delivery linking them.
func TestLogsDeliveryCLI(t *testing.T) {
	srcName := "cli-delivery-source"
	dstName := "cli-delivery-destination"
	defer runCLIIgnore(awsCLI("logs", "delete-delivery-source", "--name", srcName))
	defer runCLIIgnore(awsCLI("logs", "delete-delivery-destination", "--name", dstName))

	srcOut := runCLI(t, awsCLI("logs", "put-delivery-source",
		"--name", srcName,
		"--log-type", "APPLICATION_LOGS",
		"--resource-arn", "arn:aws:bedrock:us-east-1:123456789012:provisioned-model/abc",
		"--output", "json"))
	var src struct {
		DeliverySource struct {
			Name string `json:"name"`
			Arn  string `json:"arn"`
		} `json:"deliverySource"`
	}
	parseJSON(t, srcOut, &src)
	require.Equal(t, srcName, src.DeliverySource.Name)

	runCLI(t, awsCLI("logs", "get-delivery-source", "--name", srcName))
	runCLI(t, awsCLI("logs", "describe-delivery-sources"))

	// aws CLI 2.26.6 lacks the newer --delivery-destination-type flag; the
	// sim defaults the destination type to S3 and the SDK test covers it.
	dstOut := runCLI(t, awsCLI("logs", "put-delivery-destination",
		"--name", dstName,
		"--output-format", "json",
		"--delivery-destination-configuration", "destinationResourceArn=arn:aws:s3:::my-delivery-bucket",
		"--output", "json"))
	var dst struct {
		DeliveryDestination struct {
			Name string `json:"name"`
			Arn  string `json:"arn"`
		} `json:"deliveryDestination"`
	}
	parseJSON(t, dstOut, &dst)
	require.Equal(t, dstName, dst.DeliveryDestination.Name)
	dstArn := dst.DeliveryDestination.Arn
	require.NotEmpty(t, dstArn)

	runCLI(t, awsCLI("logs", "get-delivery-destination", "--name", dstName))
	runCLI(t, awsCLI("logs", "describe-delivery-destinations"))

	polDoc := `{"Version":"2012-10-17","Statement":[{"Sid":"Allow","Effect":"Allow","Principal":{"Service":"delivery.logs.amazonaws.com"},"Action":"logs:CreateDelivery","Resource":"*"}]}`
	runCLI(t, awsCLI("logs", "put-delivery-destination-policy",
		"--delivery-destination-name", dstName,
		"--delivery-destination-policy", polDoc))
	gpOut := runCLI(t, awsCLI("logs", "get-delivery-destination-policy",
		"--delivery-destination-name", dstName, "--output", "json"))
	var gp struct {
		Policy struct {
			DeliveryDestinationPolicy string `json:"deliveryDestinationPolicy"`
		} `json:"policy"`
	}
	parseJSON(t, gpOut, &gp)
	assert.Equal(t, polDoc, gp.Policy.DeliveryDestinationPolicy)

	cdOut := runCLI(t, awsCLI("logs", "create-delivery",
		"--delivery-source-name", srcName,
		"--delivery-destination-arn", dstArn,
		"--output", "json"))
	var cd struct {
		Delivery struct {
			Id string `json:"id"`
		} `json:"delivery"`
	}
	parseJSON(t, cdOut, &cd)
	require.NotEmpty(t, cd.Delivery.Id)

	gdOut := runCLI(t, awsCLI("logs", "get-delivery", "--id", cd.Delivery.Id, "--output", "json"))
	var gd struct {
		Delivery struct {
			DeliverySourceName string `json:"deliverySourceName"`
		} `json:"delivery"`
	}
	parseJSON(t, gdOut, &gd)
	assert.Equal(t, srcName, gd.Delivery.DeliverySourceName)

	runCLI(t, awsCLI("logs", "describe-deliveries"))
	runCLI(t, awsCLI("logs", "delete-delivery", "--id", cd.Delivery.Id))
	runCLI(t, awsCLI("logs", "delete-delivery-destination-policy",
		"--delivery-destination-name", dstName))
	runCLI(t, awsCLI("logs", "delete-delivery-destination", "--name", dstName))
	runCLI(t, awsCLI("logs", "delete-delivery-source", "--name", srcName))
}

// TestLogsAnomalyAndIndexCLI exercises log anomaly detectors, index policies,
// field indexes, and the published configuration templates.
func TestLogsAnomalyAndIndexCLI(t *testing.T) {
	group := "/cli/anomaly-index"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	lgOut := runCLI(t, awsCLI("logs", "describe-log-groups",
		"--log-group-name-prefix", group, "--output", "json"))
	var lg struct {
		LogGroups []struct {
			LogGroupName string `json:"logGroupName"`
			Arn          string `json:"arn"`
		} `json:"logGroups"`
	}
	parseJSON(t, lgOut, &lg)
	require.NotEmpty(t, lg.LogGroups)
	groupArn := lg.LogGroups[0].Arn

	adOut := runCLI(t, awsCLI("logs", "create-log-anomaly-detector",
		"--detector-name", "cli-detector",
		"--log-group-arn-list", groupArn,
		"--evaluation-frequency", "ONE_HOUR",
		"--output", "json"))
	var ad struct {
		AnomalyDetectorArn string `json:"anomalyDetectorArn"`
	}
	parseJSON(t, adOut, &ad)
	require.NotEmpty(t, ad.AnomalyDetectorArn)
	defer runCLIIgnore(awsCLI("logs", "delete-log-anomaly-detector",
		"--anomaly-detector-arn", ad.AnomalyDetectorArn))

	getAD := runCLI(t, awsCLI("logs", "get-log-anomaly-detector",
		"--anomaly-detector-arn", ad.AnomalyDetectorArn, "--output", "json"))
	var gad struct {
		DetectorName        string `json:"detectorName"`
		EvaluationFrequency string `json:"evaluationFrequency"`
	}
	parseJSON(t, getAD, &gad)
	assert.Equal(t, "cli-detector", gad.DetectorName)
	assert.Equal(t, "ONE_HOUR", gad.EvaluationFrequency)

	listAD := runCLI(t, awsCLI("logs", "list-log-anomaly-detectors", "--output", "json"))
	var lad struct {
		AnomalyDetectors []struct {
			AnomalyDetectorArn string `json:"anomalyDetectorArn"`
		} `json:"anomalyDetectors"`
	}
	parseJSON(t, listAD, &lad)
	foundAD := false
	for _, d := range lad.AnomalyDetectors {
		if d.AnomalyDetectorArn == ad.AnomalyDetectorArn {
			foundAD = true
		}
	}
	assert.True(t, foundAD, "anomaly detector should be listed")
	runCLI(t, awsCLI("logs", "delete-log-anomaly-detector",
		"--anomaly-detector-arn", ad.AnomalyDetectorArn))

	// Index policy.
	runCLI(t, awsCLI("logs", "put-index-policy",
		"--log-group-identifier", group,
		"--policy-document", `{"Fields":["requestId","accountId"]}`))
	descIdx := runCLI(t, awsCLI("logs", "describe-index-policies",
		"--log-group-identifiers", group, "--output", "json"))
	var ip struct {
		IndexPolicies []struct {
			LogGroupIdentifier string `json:"logGroupIdentifier"`
		} `json:"indexPolicies"`
	}
	parseJSON(t, descIdx, &ip)
	require.Len(t, ip.IndexPolicies, 1)
	assert.Equal(t, group, ip.IndexPolicies[0].LogGroupIdentifier)

	runCLI(t, awsCLI("logs", "describe-field-indexes", "--log-group-identifiers", group))
	runCLI(t, awsCLI("logs", "delete-index-policy", "--log-group-identifier", group))

	// Configuration templates.
	ctOut := runCLI(t, awsCLI("logs", "describe-configuration-templates", "--output", "json"))
	var ct struct {
		ConfigurationTemplates []struct {
			Service                 string `json:"service"`
			DeliveryDestinationType string `json:"deliveryDestinationType"`
		} `json:"configurationTemplates"`
	}
	parseJSON(t, ctOut, &ct)
	require.NotEmpty(t, ct.ConfigurationTemplates)
	for _, tpl := range ct.ConfigurationTemplates {
		assert.NotEmpty(t, tpl.Service)
		assert.NotEmpty(t, tpl.DeliveryDestinationType)
	}
}
