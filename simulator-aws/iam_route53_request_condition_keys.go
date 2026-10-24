package main

import (
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"
)

func init() {
	registerIAMRequestConditionPopulator("route53", iamPopulateRoute53RequestConditionKeys)
}

// iamPopulateRoute53RequestConditionKeys adds the Amazon Route 53 keys in the
// forms the Route 53 Developer Guide's "Normalizing the Route 53 condition key
// values" prescribes: a change batch's actions and record types in uppercase,
// its record names normalized, and the VPCs a request names as
// "VPCId=<vpc-id>,VPCRegion=<region>" in lowercase.
func iamPopulateRoute53RequestConditionKeys(r *http.Request, operation string, body []byte, ctx map[string][]string) {
	switch operation {
	case "ChangeResourceRecordSets":
		var request R53ChangeRRSetRequest
		if len(body) == 0 || xml.Unmarshal(body, &request) != nil {
			return
		}
		var actions, types, names []string
		for _, change := range request.ChangeBatch.Changes {
			actions = append(actions, strings.ToUpper(change.Action))
			types = append(types, strings.ToUpper(change.ResourceRecordSet.Type))
			names = append(names, route53NormalizedRecordName(change.ResourceRecordSet.Name))
		}
		iamSetConditionValues(ctx, "route53:ChangeResourceRecordSetsActions", actions...)
		iamSetConditionValues(ctx, "route53:ChangeResourceRecordSetsRecordTypes", types...)
		iamSetConditionValues(ctx, "route53:ChangeResourceRecordSetsNormalizedRecordNames", names...)
	case "CreateHostedZone", "AssociateVPCWithHostedZone", "DisassociateVPCFromHostedZone",
		"CreateVPCAssociationAuthorization", "DeleteVPCAssociationAuthorization":
		var request struct {
			VPC *R53VPC `xml:"VPC"`
		}
		if len(body) == 0 || xml.Unmarshal(body, &request) != nil || request.VPC == nil {
			return
		}
		iamSetConditionValues(ctx, "route53:VPCs", route53ConditionVPC(request.VPC.VPCId, request.VPC.VPCRegion))
	case "ListHostedZonesByVPC":
		query := r.URL.Query()
		iamSetConditionValues(ctx, "route53:VPCs", route53ConditionVPC(query.Get("vpcid"), query.Get("vpcregion")))
	}
}

func route53ConditionVPC(id, region string) string {
	if id == "" || region == "" {
		return ""
	}
	return "VPCId=" + strings.ToLower(id) + ",VPCRegion=" + strings.ToLower(region)
}

// route53NormalizedRecordName lowercases a record name, drops its trailing dot
// and writes every byte other than a-z, 0-9, hyphen, underscore and the label
// dot as a backslash and three octal digits. A name the request already
// escapes is decoded first, so "*.example.com" and "\052.example.com" both
// normalize to "\052.example.com".
func route53NormalizedRecordName(name string) string {
	var decoded []byte
	for i := 0; i < len(name); i++ {
		if name[i] == '\\' && i+3 < len(name) && isOctalDigits(name[i+1:i+4]) {
			decoded = append(decoded, (name[i+1]-'0')<<6|(name[i+2]-'0')<<3|(name[i+3]-'0'))
			i += 3
			continue
		}
		decoded = append(decoded, name[i])
	}
	lowered := strings.TrimSuffix(strings.ToLower(string(decoded)), ".")
	var b strings.Builder
	for i := 0; i < len(lowered); i++ {
		c := lowered[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, `\%03o`, c)
	}
	return b.String()
}

func isOctalDigits(s string) bool {
	if len(s) != 3 || s[0] > '3' {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '7' {
			return false
		}
	}
	return true
}
