package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Regression net for XML wire shapes the Smithy models pin down but SDK
// deserializers silently tolerate (they don't key on root element names
// or unknown elements). The runtime spec validator flags these classes
// when armed; these assertions keep the fixed shapes pinned in plain
// test runs too.

// TestS3BucketConfigurationListRoots pins the response root elements of
// the four bucket configuration-list operations to the models'
// xmlName / shape-name serialization (s3.smithy.json.gz): analytics
// lists serialize as the singular <ListBucketAnalyticsConfigurationResult>,
// inventory and metrics drop the "Bucket" infix, and intelligent-tiering
// (no xmlName trait) takes the output shape name.
func TestS3BucketConfigurationListRoots(t *testing.T) {
	srv, _, _ := buildConformanceSimulator(t)

	put := httptest.NewRequest("PUT", "/shape-probe-bucket", nil)
	put.SetPathValue("bucket", "shape-probe-bucket")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, put)
	if rr.Code != 200 {
		t.Fatalf("CreateBucket: status %d: %s", rr.Code, rr.Body.String())
	}

	for sub, wantRoot := range map[string]string{
		"analytics":           "ListBucketAnalyticsConfigurationResult",
		"inventory":           "ListInventoryConfigurationsResult",
		"metrics":             "ListMetricsConfigurationsResult",
		"intelligent-tiering": "ListBucketIntelligentTieringConfigurationsOutput",
	} {
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, httptest.NewRequest("GET", "/shape-probe-bucket?"+sub, nil))
		if rr.Code != 200 {
			t.Errorf("GET ?%s: status %d: %s", sub, rr.Code, rr.Body.String())
			continue
		}
		root, err := parseXMLTree(rr.Body.Bytes())
		if err != nil {
			t.Errorf("GET ?%s: %v", sub, err)
			continue
		}
		if root.name != wantRoot {
			t.Errorf("GET ?%s: root element <%s>, want <%s>", sub, root.name, wantRoot)
		}
	}
}

// TestEC2DescribeInstanceTypesVCpuInfoCasing pins the vCpuInfo element
// to the model's xmlName ("vCpuInfo", capital C — ec2.smithy.json.gz
// InstanceTypeInfo.VCpuInfo).
func TestEC2DescribeInstanceTypesVCpuInfoCasing(t *testing.T) {
	srv, _, _ := buildConformanceSimulator(t)

	req := httptest.NewRequest("POST", "/", strings.NewReader("Action=DescribeInstanceTypes&Version=2016-11-15"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signSeedControlPlane(req)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("DescribeInstanceTypes: status %d: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if strings.Contains(body, "<vcpuInfo>") {
		t.Fatal("DescribeInstanceTypes emits <vcpuInfo>; the EC2 model serializes the member as <vCpuInfo>")
	}
	if !strings.Contains(body, "<vCpuInfo>") {
		t.Fatal("DescribeInstanceTypes response carries no <vCpuInfo> element")
	}
}
