package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These tests exercise the CloudFront restXml surface added for distribution
// tenants, connection groups, trust stores, resource policies, WebACL
// associations, alias/domain conflicts, the ListDistributionsBy* projections,
// and the CopyDistribution / UpdateDistributionWithStagingConfig /
// UpdateAnycastIpList / GetManagedCertificateDetails variants — via the real
// `aws` CLI. The CLI accepts JSON input and emits JSON output even though the
// wire is restXml.

// cfExtras3DistConfig returns a minimal enabled DistributionConfig JSON document
// the create-distribution CLI accepts.
func cfExtras3DistConfig(caller string) string {
	return fmt.Sprintf(`{
		"CallerReference":"%s",
		"Comment":"cli extras3",
		"Enabled":true,
		"Origins":{"Quantity":1,"Items":[{"Id":"o1","DomainName":"example.com","CustomOriginConfig":{"HTTPPort":80,"HTTPSPort":443,"OriginProtocolPolicy":"http-only","OriginSslProtocols":{"Quantity":1,"Items":["TLSv1.2"]}}}]},
		"DefaultCacheBehavior":{"TargetOriginId":"o1","ViewerProtocolPolicy":"allow-all","ForwardedValues":{"QueryString":false,"Cookies":{"Forward":"none"}},"MinTTL":0},
		"ViewerCertificate":{"CloudFrontDefaultCertificate":true},
		"Restrictions":{"GeoRestriction":{"RestrictionType":"none","Quantity":0}}
	}`, caller)
}

// cfExtras3CreateDistributionCLI creates a distribution and returns its id + ETag,
// registering a tolerant cleanup that disables then deletes it.
func cfExtras3CreateDistributionCLI(t *testing.T) (string, string) {
	t.Helper()
	caller := "cli-extras3-" + time.Now().Format("150405.000000000")
	out := runCLI(t, awsCLI("cloudfront", "create-distribution",
		"--distribution-config", cfExtras3DistConfig(caller), "--output", "json"))
	var create struct {
		Distribution struct {
			Id string `json:"Id"`
		} `json:"Distribution"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.Distribution.Id
	require.NotEmpty(t, id)
	t.Cleanup(func() { cfExtras3DeleteDistributionCLI(id) })
	return id, create.ETag
}

// cfExtras3DeleteDistributionCLI disables then deletes a distribution, ignoring
// errors (tolerant teardown).
func cfExtras3DeleteDistributionCLI(id string) {
	g := runCLIIgnoreErr(awsCLI("cloudfront", "get-distribution", "--id", id, "--output", "json"))
	var gr struct {
		Distribution struct {
			DistributionConfig json.RawMessage `json:"DistributionConfig"`
		} `json:"Distribution"`
		ETag string `json:"ETag"`
	}
	if json.Unmarshal([]byte(g), &gr) != nil || gr.ETag == "" {
		return
	}
	var cfg map[string]any
	if json.Unmarshal(gr.Distribution.DistributionConfig, &cfg) != nil {
		return
	}
	cfg["Enabled"] = false
	cfgJSON, _ := json.Marshal(cfg)
	u := runCLIIgnoreErr(awsCLI("cloudfront", "update-distribution", "--id", id,
		"--if-match", gr.ETag, "--distribution-config", string(cfgJSON), "--output", "json"))
	var ur struct {
		ETag string `json:"ETag"`
	}
	if json.Unmarshal([]byte(u), &ur) == nil && ur.ETag != "" {
		runCLIIgnore(awsCLI("cloudfront", "delete-distribution", "--id", id, "--if-match", ur.ETag))
	}
}

// TestCloudFrontCopyAndStagingCLI covers copy-distribution and
// update-distribution-with-staging-config.
func TestCloudFrontCopyAndStagingCLI(t *testing.T) {
	id, etag := cfExtras3CreateDistributionCLI(t)

	out := runCLI(t, awsCLI("cloudfront", "copy-distribution",
		"--primary-distribution-id", id, "--if-match", etag,
		"--caller-reference", "cli-copy-"+time.Now().Format("150405.000000000"),
		"--staging", "--output", "json"))
	var cp struct {
		Distribution struct {
			Id string `json:"Id"`
		} `json:"Distribution"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &cp))
	stagingID := cp.Distribution.Id
	require.NotEmpty(t, stagingID)
	t.Cleanup(func() { cfExtras3DeleteDistributionCLI(stagingID) })

	g := runCLI(t, awsCLI("cloudfront", "get-distribution", "--id", id, "--output", "json"))
	var gr struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(g), &gr))
	pr := runCLI(t, awsCLI("cloudfront", "update-distribution-with-staging-config",
		"--id", id, "--staging-distribution-id", stagingID, "--if-match", gr.ETag, "--output", "json"))
	var prr struct {
		Distribution struct {
			Id string `json:"Id"`
		} `json:"Distribution"`
	}
	require.NoError(t, json.Unmarshal([]byte(pr), &prr))
	require.Equal(t, id, prr.Distribution.Id)
}

// TestCloudFrontDistributionTenantCLI covers the distribution-tenant CRUD,
// by-domain lookup, list, WebACL associate/disassociate, the tenant invalidation
// trio, and get-managed-certificate-details.
func TestCloudFrontDistributionTenantCLI(t *testing.T) {
	distID, _ := cfExtras3CreateDistributionCLI(t)
	domain := "cli-tenant-" + time.Now().Format("150405") + ".example.com"

	out := runCLI(t, awsCLI("cloudfront", "create-distribution-tenant",
		"--distribution-id", distID,
		"--name", "cli-tenant-"+time.Now().Format("150405"),
		"--domains", fmt.Sprintf(`[{"Domain":"%s"}]`, domain),
		"--output", "json"))
	var create struct {
		DistributionTenant struct {
			Id string `json:"Id"`
		} `json:"DistributionTenant"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	tenantID := create.DistributionTenant.Id
	require.NotEmpty(t, tenantID)
	etag := create.ETag
	t.Cleanup(func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-distribution-tenant", "--identifier", tenantID, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-distribution-tenant", "--id", tenantID, "--if-match", gr.ETag))
		}
	})

	runCLI(t, awsCLI("cloudfront", "get-distribution-tenant", "--identifier", tenantID, "--output", "json"))

	bd := runCLI(t, awsCLI("cloudfront", "get-distribution-tenant-by-domain", "--domain", domain, "--output", "json"))
	var bdr struct {
		DistributionTenant struct {
			Id string `json:"Id"`
		} `json:"DistributionTenant"`
	}
	require.NoError(t, json.Unmarshal([]byte(bd), &bdr))
	require.Equal(t, tenantID, bdr.DistributionTenant.Id)

	u := runCLI(t, awsCLI("cloudfront", "update-distribution-tenant",
		"--id", tenantID, "--if-match", etag, "--no-enabled", "--output", "json"))
	var ur struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(u), &ur))
	etag = ur.ETag

	lt := runCLI(t, awsCLI("cloudfront", "list-distribution-tenants", "--output", "json"))
	var ltr struct {
		DistributionTenantList []cfIDItem `json:"DistributionTenantList"`
	}
	require.NoError(t, json.Unmarshal([]byte(lt), &ltr))
	require.True(t, cfExtras2Contains(ltr.DistributionTenantList, tenantID))

	runCLI(t, awsCLI("cloudfront", "list-distribution-tenants-by-customization", "--output", "json"))

	aw := runCLI(t, awsCLI("cloudfront", "associate-distribution-tenant-web-acl",
		"--id", tenantID, "--web-acl-arn", "arn:aws:wafv2:us-east-1:111122223333:global/webacl/t/abc",
		"--if-match", etag, "--output", "json"))
	var awr struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(aw), &awr))

	dw := runCLI(t, awsCLI("cloudfront", "disassociate-distribution-tenant-web-acl",
		"--id", tenantID, "--if-match", awr.ETag, "--output", "json"))
	var dwr struct {
		Id string `json:"Id"`
	}
	require.NoError(t, json.Unmarshal([]byte(dw), &dwr))
	require.Equal(t, tenantID, dwr.Id)

	ci := runCLI(t, awsCLI("cloudfront", "create-invalidation-for-distribution-tenant",
		"--id", tenantID,
		"--invalidation-batch", fmt.Sprintf(`{"CallerReference":"cli-inv-%s","Paths":{"Quantity":1,"Items":["/*"]}}`, time.Now().Format("150405.000000")),
		"--output", "json"))
	var cir struct {
		Invalidation struct {
			Id string `json:"Id"`
		} `json:"Invalidation"`
	}
	require.NoError(t, json.Unmarshal([]byte(ci), &cir))
	invID := cir.Invalidation.Id
	require.NotEmpty(t, invID)

	gi := runCLI(t, awsCLI("cloudfront", "get-invalidation-for-distribution-tenant",
		"--distribution-tenant-id", tenantID, "--id", invID, "--output", "json"))
	var gir struct {
		Invalidation struct {
			Id string `json:"Id"`
		} `json:"Invalidation"`
	}
	require.NoError(t, json.Unmarshal([]byte(gi), &gir))
	require.Equal(t, invID, gir.Invalidation.Id)

	runCLI(t, awsCLI("cloudfront", "list-invalidations-for-distribution-tenant", "--id", tenantID, "--output", "json"))

	runCLI(t, awsCLI("cloudfront", "get-managed-certificate-details", "--identifier", tenantID, "--output", "json"))
}

// TestCloudFrontConnectionGroupCLI covers connection-group CRUD,
// by-routing-endpoint lookup, and list.
func TestCloudFrontConnectionGroupCLI(t *testing.T) {
	out := runCLI(t, awsCLI("cloudfront", "create-connection-group",
		"--name", "cli-cg-"+time.Now().Format("150405.000"), "--enabled", "--output", "json"))
	var create struct {
		ConnectionGroup struct {
			Id              string `json:"Id"`
			RoutingEndpoint string `json:"RoutingEndpoint"`
		} `json:"ConnectionGroup"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.ConnectionGroup.Id
	require.NotEmpty(t, id)
	etag := create.ETag
	endpoint := create.ConnectionGroup.RoutingEndpoint
	t.Cleanup(func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-connection-group", "--identifier", id, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-connection-group", "--id", id, "--if-match", gr.ETag))
		}
	})

	runCLI(t, awsCLI("cloudfront", "get-connection-group", "--identifier", id, "--output", "json"))

	be := runCLI(t, awsCLI("cloudfront", "get-connection-group-by-routing-endpoint",
		"--routing-endpoint", endpoint, "--output", "json"))
	var ber struct {
		ConnectionGroup struct {
			Id string `json:"Id"`
		} `json:"ConnectionGroup"`
	}
	require.NoError(t, json.Unmarshal([]byte(be), &ber))
	require.Equal(t, id, ber.ConnectionGroup.Id)

	runCLI(t, awsCLI("cloudfront", "update-connection-group",
		"--id", id, "--if-match", etag, "--ipv6-enabled", "--output", "json"))

	lc := runCLI(t, awsCLI("cloudfront", "list-connection-groups", "--output", "json"))
	var lcr struct {
		ConnectionGroups []cfIDItem `json:"ConnectionGroups"`
	}
	require.NoError(t, json.Unmarshal([]byte(lc), &lcr))
	require.True(t, cfExtras2Contains(lcr.ConnectionGroups, id))
}

// TestCloudFrontTrustStoreCLI covers trust-store CRUD, list, and
// list-distributions-by-trust-store.
func TestCloudFrontTrustStoreCLI(t *testing.T) {
	bundle := `{"CaCertificatesBundleS3Location":{"Bucket":"my-bucket","Key":"ca-bundle.pem","Region":"us-east-1"}}`
	out := runCLI(t, awsCLI("cloudfront", "create-trust-store",
		"--name", "cli-ts-"+time.Now().Format("150405.000"),
		"--ca-certificates-bundle-source", bundle, "--output", "json"))
	var create struct {
		TrustStore struct {
			Id string `json:"Id"`
		} `json:"TrustStore"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.TrustStore.Id
	require.NotEmpty(t, id)
	etag := create.ETag
	t.Cleanup(func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-trust-store", "--identifier", id, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-trust-store", "--id", id, "--if-match", gr.ETag))
		}
	})

	runCLI(t, awsCLI("cloudfront", "get-trust-store", "--identifier", id, "--output", "json"))

	bundle2 := `{"CaCertificatesBundleS3Location":{"Bucket":"my-bucket","Key":"ca-bundle-2.pem","Region":"us-east-1"}}`
	runCLI(t, awsCLI("cloudfront", "update-trust-store",
		"--id", id, "--if-match", etag, "--ca-certificates-bundle-source", bundle2, "--output", "json"))

	ls := runCLI(t, awsCLI("cloudfront", "list-trust-stores", "--output", "json"))
	var lsr struct {
		TrustStoreList []cfIDItem `json:"TrustStoreList"`
	}
	require.NoError(t, json.Unmarshal([]byte(ls), &lsr))
	require.True(t, cfExtras2Contains(lsr.TrustStoreList, id))

	runCLI(t, awsCLI("cloudfront", "list-distributions-by-trust-store",
		"--trust-store-identifier", id, "--output", "json"))
}

// TestCloudFrontResourcePolicyCLI covers put/get/delete-resource-policy.
func TestCloudFrontResourcePolicyCLI(t *testing.T) {
	arn := "arn:aws:cloudfront::111122223333:distribution-tenant/DT" + time.Now().Format("150405")
	doc := `{"Version":"2012-10-17","Statement":[]}`

	pp := runCLI(t, awsCLI("cloudfront", "put-resource-policy",
		"--resource-arn", arn, "--policy-document", doc, "--output", "json"))
	var ppr struct {
		ResourceArn string `json:"ResourceArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(pp), &ppr))
	require.Equal(t, arn, ppr.ResourceArn)

	gp := runCLI(t, awsCLI("cloudfront", "get-resource-policy", "--resource-arn", arn, "--output", "json"))
	var gpr struct {
		PolicyDocument string `json:"PolicyDocument"`
	}
	require.NoError(t, json.Unmarshal([]byte(gp), &gpr))
	require.Equal(t, doc, gpr.PolicyDocument)

	runCLI(t, awsCLI("cloudfront", "delete-resource-policy", "--resource-arn", arn))
}

// TestCloudFrontWebACLAndAliasCLI covers distribution WebACL associate/disassociate,
// associate-alias, list-conflicting-aliases, list-domain-conflicts,
// update-domain-association, and verify-dns-configuration.
func TestCloudFrontWebACLAndAliasCLI(t *testing.T) {
	id, etag := cfExtras3CreateDistributionCLI(t)

	aw := runCLI(t, awsCLI("cloudfront", "associate-distribution-web-acl",
		"--id", id, "--web-acl-arn", "arn:aws:wafv2:us-east-1:111122223333:global/webacl/d/abc",
		"--if-match", etag, "--output", "json"))
	var awr struct {
		Id   string `json:"Id"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(aw), &awr))
	require.Equal(t, id, awr.Id)

	dw := runCLI(t, awsCLI("cloudfront", "disassociate-distribution-web-acl",
		"--id", id, "--if-match", awr.ETag, "--output", "json"))
	var dwr struct {
		Id string `json:"Id"`
	}
	require.NoError(t, json.Unmarshal([]byte(dw), &dwr))
	require.Equal(t, id, dwr.Id)

	alias := "cli-alias-" + time.Now().Format("150405") + ".example.com"
	runCLI(t, awsCLI("cloudfront", "associate-alias", "--target-distribution-id", id, "--alias", alias))

	other, _ := cfExtras3CreateDistributionCLI(t)
	runCLI(t, awsCLI("cloudfront", "list-conflicting-aliases",
		"--distribution-id", other, "--alias", alias, "--output", "json"))

	runCLI(t, awsCLI("cloudfront", "list-domain-conflicts",
		"--domain", alias,
		"--domain-control-validation-resource", fmt.Sprintf(`{"DistributionId":"%s"}`, id),
		"--output", "json"))

	uda := runCLI(t, awsCLI("cloudfront", "update-domain-association",
		"--domain", alias,
		"--target-resource", fmt.Sprintf(`{"DistributionId":"%s"}`, id),
		"--output", "json"))
	var udar struct {
		Domain string `json:"Domain"`
	}
	require.NoError(t, json.Unmarshal([]byte(uda), &udar))
	require.Equal(t, alias, udar.Domain)

	runCLI(t, awsCLI("cloudfront", "verify-dns-configuration",
		"--identifier", id, "--domain", alias, "--output", "json"))
}

// TestCloudFrontListDistributionsByResourceCLI covers the ListDistributionsBy*
// projections over the existing distribution store.
func TestCloudFrontListDistributionsByResourceCLI(t *testing.T) {
	_, _ = cfExtras3CreateDistributionCLI(t)

	runCLI(t, awsCLI("cloudfront", "list-distributions-by-cache-policy-id", "--cache-policy-id", "cp-123", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-origin-request-policy-id", "--origin-request-policy-id", "orp-123", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-response-headers-policy-id", "--response-headers-policy-id", "rhp-123", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-key-group", "--key-group-id", "kg-123", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-vpc-origin-id", "--vpc-origin-id", "vo-123", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-realtime-log-config", "--realtime-log-config-name", "rtl-123", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-anycast-ip-list-id", "--anycast-ip-list-id", "aipl-123", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-web-acl-id", "--web-acl-id", "webacl-123", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-connection-mode", "--connection-mode", "direct", "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "list-distributions-by-owned-resource", "--resource-arn", "arn:aws:wafv2:us-east-1:111122223333:global/webacl/x/y", "--output", "json"))
}

// TestCloudFrontUpdateAnycastIpListCLI covers update-anycast-ip-list.
func TestCloudFrontUpdateAnycastIpListCLI(t *testing.T) {
	out := runCLI(t, awsCLI("cloudfront", "create-anycast-ip-list",
		"--name", "cli-aipl-"+time.Now().Format("150405.000"), "--ip-count", "2", "--output", "json"))
	var create struct {
		AnycastIpList struct {
			Id string `json:"Id"`
		} `json:"AnycastIpList"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.AnycastIpList.Id
	require.NotEmpty(t, id)
	etag := create.ETag
	t.Cleanup(func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-anycast-ip-list", "--id", id, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-anycast-ip-list", "--id", id, "--if-match", gr.ETag))
		}
	})

	u := runCLI(t, awsCLI("cloudfront", "update-anycast-ip-list",
		"--id", id, "--if-match", etag, "--output", "json"))
	var ur struct {
		AnycastIpList struct {
			Id string `json:"Id"`
		} `json:"AnycastIpList"`
	}
	require.NoError(t, json.Unmarshal([]byte(u), &ur))
	require.Equal(t, id, ur.AnycastIpList.Id)
}
