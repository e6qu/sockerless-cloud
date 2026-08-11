package main

import (
	"net/http"
	"strings"
	"testing"
)

// FuzzEC2FormFlatten drives the AWS query-protocol form flatteners
// (parseLaunchTemplateData, ec2Filters, ec2ParamList, snsPublishBatchEntries)
// with an arbitrary url-encoded form body. These flatteners walk
// attacker-supplied `.N.` / `.member.N.` indexed keys; none may panic, hang,
// or OOM on a malformed, sparse, or huge-index key set.
func FuzzEC2FormFlatten(f *testing.F) {
	seeds := []string{
		"",
		"LaunchTemplateData.ImageId=ami-1",
		"LaunchTemplateData.NetworkInterface.1.DeviceIndex=0",
		"LaunchTemplateData.NetworkInterface.1.SecurityGroupId.1=sg-1",
		"LaunchTemplateData.BlockDeviceMapping.1.Ebs.VolumeSize=8",
		"LaunchTemplateData.TagSpecification.1.Tag.1.Key=k&LaunchTemplateData.TagSpecification.1.Tag.1.Value=v",
		"Filter.1.Name=tag&Filter.1.Value.1=x",
		"Filter.1.Name=&Filter.1.Value.1=x",
		"Filter.999999999.Name=x",
		"LaunchTemplateData.NetworkInterface.999999999.DeviceIndex=0",
		"PublishBatchRequestEntries.member.1.Id=a&PublishBatchRequestEntries.member.1.Message=m",
		"PublishBatchRequestEntries.member.99999999.Id=a",
		"LaunchTemplateData.SecurityGroupId.1=&LaunchTemplateData.SecurityGroupId.2=x",
		"LaunchTemplateData.MetadataOptions.HttpPutResponseHopLimit=notanumber",
		"a.b.c.d.e.f.g.h.i.j.k=1",
		"=&=&=",
		"LaunchTemplateData.Monitoring.Enabled=" + strings.Repeat("a", 1000),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, form string) {
		req, err := http.NewRequest(http.MethodPost, "http://sim/", strings.NewReader(form))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		// Each of these forces ParseForm internally and walks indexed keys.
		_ = parseLaunchTemplateData(req, "LaunchTemplateData")
		_ = ec2Filters(req)
		_ = ec2ParamList(req, "LaunchTemplateData.SecurityGroupId")
		_ = snsPublishBatchEntries(req)
	})
}
