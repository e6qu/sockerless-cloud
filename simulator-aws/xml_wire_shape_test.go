package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// SDK deserializers ignore root element names and unknown elements, so these
// tests pin the XML wire shapes the Smithy models declare; the runtime spec
// validator checks the same classes only when armed.

// TestS3BucketConfigurationListRoots pins the response roots of the four bucket
// configuration-list operations to the xmlName or shape name s3.smithy.json.gz
// declares: analytics serializes as the singular
// <ListBucketAnalyticsConfigurationResult>, inventory and metrics drop the
// "Bucket" infix, and intelligent-tiering, with no xmlName, takes the output
// shape name.
func TestS3BucketConfigurationListRoots(t *testing.T) {
	srv, _, _ := buildConformanceSimulator(t)

	put := httptest.NewRequest("PUT", "/shape-probe-bucket", nil)
	put.SetPathValue("bucket", "shape-probe-bucket")
	signSeedS3(put)
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
		srv.ServeHTTP(rr, s3SeedReq("GET", "/shape-probe-bucket?"+sub, nil))
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

// TestEC2DescribeInstanceTypesVCpuInfoCasing pins the vCpuInfo element to the
// xmlName ec2.smithy.json.gz declares on InstanceTypeInfo.VCpuInfo.
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
