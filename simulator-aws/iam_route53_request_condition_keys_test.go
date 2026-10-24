package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func route53ConditionContext(r *http.Request, operation, body string) map[string][]string {
	if r == nil {
		r = httptest.NewRequest(http.MethodPost, "/2013-04-01/", nil)
	}
	return populatedConditionContext(r, "route53", operation, body)
}

func TestRoute53ConditionKeysReadTheChangeBatch(t *testing.T) {
	ctx := route53ConditionContext(nil, "ChangeResourceRecordSets", `<?xml version="1.0" encoding="UTF-8"?>
<ChangeResourceRecordSetsRequest xmlns="https://route53.amazonaws.com/doc/2013-04-01/">
  <ChangeBatch><Changes>
    <Change><Action>CREATE</Action><ResourceRecordSet><Name>WWW.Example.com.</Name><Type>A</Type></ResourceRecordSet></Change>
    <Change><Action>UPSERT</Action><ResourceRecordSet><Name>*.example.com</Name><Type>TXT</Type></ResourceRecordSet></Change>
    <Change><Action>DELETE</Action><ResourceRecordSet><Name>\052.Example.COM.</Name><Type>TXT</Type></ResourceRecordSet></Change>
  </Changes></ChangeBatch>
</ChangeResourceRecordSetsRequest>`)
	assertPopulatedConditionValues(t, ctx, map[string][]string{
		"route53:ChangeResourceRecordSetsActions":               {"CREATE", "UPSERT", "DELETE"},
		"route53:ChangeResourceRecordSetsRecordTypes":           {"A", "TXT"},
		"route53:ChangeResourceRecordSetsNormalizedRecordNames": {"www.example.com", `\052.example.com`},
	})
}

// The Route 53 Developer Guide's normalization: lowercase, no trailing dot,
// and every character but a-z, 0-9, hyphen, underscore and the label dot as
// a backslash and three octal digits.
func TestRoute53NormalizedRecordName(t *testing.T) {
	for name, want := range map[string]string{
		"Example.COM.":           "example.com",
		"example.com":            "example.com",
		"_dmarc.Mail-1.example.": "_dmarc.mail-1.example",
		"*.example.com.":         `\052.example.com`,
		`\052.example.com.`:      `\052.example.com`,
		"a b@example.com":        `a\040b\100example.com`,
		`\101.example.com`:       "a.example.com",
	} {
		if got := route53NormalizedRecordName(name); got != want {
			t.Errorf("route53NormalizedRecordName(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRoute53ConditionKeysReadTheVPC(t *testing.T) {
	const vpc = `<VPC><VPCRegion>US-EAST-1</VPCRegion><VPCId>VPC-0ABC</VPCId></VPC>`
	want := map[string][]string{"route53:VPCs": {"VPCId=vpc-0abc,VPCRegion=us-east-1"}}
	for operation, root := range map[string]string{
		"CreateHostedZone":                  "CreateHostedZoneRequest",
		"AssociateVPCWithHostedZone":        "AssociateVPCWithHostedZoneRequest",
		"DisassociateVPCFromHostedZone":     "DisassociateVPCFromHostedZoneRequest",
		"CreateVPCAssociationAuthorization": "CreateVPCAssociationAuthorizationRequest",
		"DeleteVPCAssociationAuthorization": "DeleteVPCAssociationAuthorizationRequest",
	} {
		ctx := route53ConditionContext(nil, operation,
			`<`+root+` xmlns="https://route53.amazonaws.com/doc/2013-04-01/">`+vpc+`</`+root+`>`)
		assertPopulatedConditionValues(t, ctx, want)
	}

	list := httptest.NewRequest(http.MethodGet, "/2013-04-01/hostedzonesbyvpc?vpcid=vpc-0abc&vpcregion=us-east-1", nil)
	assertPopulatedConditionValues(t, route53ConditionContext(list, "ListHostedZonesByVPC", ""), want)
}

func TestRoute53ConditionKeysAbsentWithoutTheirMembers(t *testing.T) {
	ctx := route53ConditionContext(nil, "CreateHostedZone",
		`<CreateHostedZoneRequest><Name>example.com</Name><CallerReference>a</CallerReference></CreateHostedZoneRequest>`)
	assertConditionKeysAbsent(t, ctx, "route53:VPCs")
	ctx = route53ConditionContext(nil, "GetHostedZone", "")
	assertConditionKeysAbsent(t, ctx, "route53:VPCs", "route53:ChangeResourceRecordSetsActions")
}
