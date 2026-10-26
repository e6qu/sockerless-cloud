package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// A default VPC is /16 with a /20 default subnet per zone, and
// CreateDefaultSubnet takes the next free /20 once a zone's default subnet is
// gone.
func TestEC2DefaultVpcGetsADefaultSubnetPerZone(t *testing.T) {
	// Let earlier background work finish before replacing its stores.
	bg.Await()
	ec2Vpcs = sim.MakeStore[EC2Vpc](nil, "ec2_vpcs")
	ec2Subnets = sim.MakeStore[EC2Subnet](nil, "ec2_subnets")
	ec2VpcCidrAssocs = sim.MakeStore[EC2VpcCidrAssoc](nil, "ec2_vpc_cidr_assocs")
	ec2VpcEncryptionControls = sim.MakeStore[EC2VpcEncryptionControl](nil, "ec2_vpc_encryption_controls")

	call := func(handler http.HandlerFunc, params url.Values) (int, string) {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(params.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		handler(w, r)
		return w.Code, w.Body.String()
	}
	code, body := call(handleCreateDefaultVpc, url.Values{"Action": {"CreateDefaultVpc"}})
	if code != http.StatusOK {
		t.Fatalf("CreateDefaultVpc: %d %s", code, body)
	}
	vpcID := regexp.MustCompile(`<vpcId>([^<]+)</vpcId>`).FindStringSubmatch(body)[1]
	defaults := ec2Subnets.Filter(func(s EC2Subnet) bool { return s.VpcId == vpcID && s.DefaultForAz })
	if len(defaults) != len(awsAvailabilityZones()) {
		t.Fatalf("default subnets: got %d, want one per zone (%d)", len(defaults), len(awsAvailabilityZones()))
	}
	blocks := map[string]string{}
	for _, s := range defaults {
		blocks[s.AvailabilityZone] = s.CidrBlock
	}
	want := map[string]string{"us-east-1a": "172.31.0.0/20", "us-east-1b": "172.31.16.0/20", "us-east-1c": "172.31.32.0/20"}
	for zone, cidr := range want {
		if blocks[zone] != cidr {
			t.Errorf("zone %s default subnet: got %q, want %s", zone, blocks[zone], cidr)
		}
	}

	code, body = call(handleCreateDefaultSubnet, url.Values{"Action": {"CreateDefaultSubnet"}, "AvailabilityZone": {"us-east-1b"}})
	if code != http.StatusBadRequest || !strings.Contains(body, "DefaultSubnetAlreadyExistsInAvailabilityZone") {
		t.Fatalf("a second default subnet in a zone: %d %s", code, body)
	}
	for _, s := range defaults {
		if s.AvailabilityZone == "us-east-1b" {
			ec2Subnets.Delete(s.SubnetId)
		}
	}
	// A non-default subnet holds the freed /20, so the new default takes the next.
	ec2Subnets.Put("subnet-held", EC2Subnet{SubnetId: "subnet-held", VpcId: vpcID, CidrBlock: "172.31.16.0/24", AvailabilityZone: "us-east-1a", State: "available"})
	code, body = call(handleCreateDefaultSubnet, url.Values{"Action": {"CreateDefaultSubnet"}, "AvailabilityZoneId": {ec2AvailabilityZoneId("us-east-1b")}})
	if code != http.StatusOK {
		t.Fatalf("CreateDefaultSubnet: %d %s", code, body)
	}
	for _, want := range []string{"<cidrBlock>172.31.48.0/20</cidrBlock>", "<defaultForAz>true</defaultForAz>", "<availabilityZone>us-east-1b</availabilityZone>"} {
		if !strings.Contains(body, want) {
			t.Errorf("CreateDefaultSubnet answer lacks %s:\n%s", want, body)
		}
	}
}
