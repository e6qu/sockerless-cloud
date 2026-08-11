package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These tests exercise the CloudFront field-level-encryption, key-value-store,
// real-time-log, streaming-distribution, VPC-origin, and Anycast-IP-list REST+XML
// surface via the real `aws` CLI. The CLI accepts JSON on input and emits JSON
// output even though the wire is XML.

// TestCloudFrontFieldLevelEncryptionCLILifecycle covers the FLE config CRUD.
func TestCloudFrontFieldLevelEncryptionCLILifecycle(t *testing.T) {
	caller := "cli-fle-" + time.Now().Format("150405.000000")
	cfg := fmt.Sprintf(`{"CallerReference":"%s","Comment":"cli fle","QueryArgProfileConfig":{"ForwardWhenQueryArgProfileIsUnknown":true},"ContentTypeProfileConfig":{"ForwardWhenContentTypeIsUnknown":true}}`, caller)

	out := runCLI(t, awsCLI("cloudfront", "create-field-level-encryption-config",
		"--field-level-encryption-config", cfg, "--output", "json"))
	var create struct {
		FieldLevelEncryption struct {
			Id string `json:"Id"`
		} `json:"FieldLevelEncryption"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.FieldLevelEncryption.Id
	require.NotEmpty(t, id)
	etag := create.ETag
	require.NotEmpty(t, etag)
	defer func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-field-level-encryption", "--id", id, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-field-level-encryption-config", "--id", id, "--if-match", gr.ETag))
		}
	}()

	out = runCLI(t, awsCLI("cloudfront", "get-field-level-encryption", "--id", id, "--output", "json"))
	var get struct {
		FieldLevelEncryption struct {
			FieldLevelEncryptionConfig struct {
				Comment string `json:"Comment"`
			} `json:"FieldLevelEncryptionConfig"`
		} `json:"FieldLevelEncryption"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &get))
	require.Equal(t, "cli fle", get.FieldLevelEncryption.FieldLevelEncryptionConfig.Comment)
	require.Equal(t, etag, get.ETag)

	runCLI(t, awsCLI("cloudfront", "get-field-level-encryption-config", "--id", id, "--output", "json"))

	updCfg := fmt.Sprintf(`{"CallerReference":"%s","Comment":"cli fle updated"}`, caller)
	out = runCLI(t, awsCLI("cloudfront", "update-field-level-encryption-config",
		"--id", id, "--if-match", etag, "--field-level-encryption-config", updCfg, "--output", "json"))
	var upd struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &upd))
	require.NotEmpty(t, upd.ETag)

	out = runCLI(t, awsCLI("cloudfront", "list-field-level-encryption-configs", "--output", "json"))
	var list struct {
		FieldLevelEncryptionList struct {
			Items []cfIDItem `json:"Items"`
		} `json:"FieldLevelEncryptionList"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	require.True(t, cfExtras2Contains(list.FieldLevelEncryptionList.Items, id))

	runCLI(t, awsCLI("cloudfront", "delete-field-level-encryption-config", "--id", id, "--if-match", upd.ETag))
}

// TestCloudFrontFieldLevelEncryptionProfileCLILifecycle covers the FLE profile CRUD.
func TestCloudFrontFieldLevelEncryptionProfileCLILifecycle(t *testing.T) {
	// Seed a public key the profile references.
	pkCaller := "cli-pk-" + time.Now().Format("150405.000000")
	pkName := "cli-fle-pk-" + time.Now().Format("150405000")
	pkCfg := fmt.Sprintf(`{"CallerReference":"%s","Name":"%s","EncodedKey":"-----BEGIN PUBLIC KEY-----\nMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAtest\n-----END PUBLIC KEY-----\n"}`, pkCaller, pkName)
	pkOut := runCLI(t, awsCLI("cloudfront", "create-public-key", "--public-key-config", pkCfg, "--output", "json"))
	var pk struct {
		PublicKey struct {
			Id string `json:"Id"`
		} `json:"PublicKey"`
	}
	require.NoError(t, json.Unmarshal([]byte(pkOut), &pk))
	pkID := pk.PublicKey.Id
	require.NotEmpty(t, pkID)
	defer func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-public-key", "--id", pkID, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-public-key", "--id", pkID, "--if-match", gr.ETag))
		}
	}()

	caller := "cli-flep-" + time.Now().Format("150405.000000")
	name := "cli-fle-profile-" + time.Now().Format("150405000")
	cfg := fmt.Sprintf(`{"Name":"%s","CallerReference":"%s","Comment":"cli profile","EncryptionEntities":{"Quantity":1,"Items":[{"PublicKeyId":"%s","ProviderId":"prov-1","FieldPatterns":{"Quantity":1,"Items":["SSN"]}}]}}`, name, caller, pkID)
	out := runCLI(t, awsCLI("cloudfront", "create-field-level-encryption-profile",
		"--field-level-encryption-profile-config", cfg, "--output", "json"))
	var create struct {
		FieldLevelEncryptionProfile struct {
			Id string `json:"Id"`
		} `json:"FieldLevelEncryptionProfile"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.FieldLevelEncryptionProfile.Id
	require.NotEmpty(t, id)
	etag := create.ETag
	defer func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-field-level-encryption-profile", "--id", id, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-field-level-encryption-profile", "--id", id, "--if-match", gr.ETag))
		}
	}()

	runCLI(t, awsCLI("cloudfront", "get-field-level-encryption-profile", "--id", id, "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "get-field-level-encryption-profile-config", "--id", id, "--output", "json"))

	updCfg := fmt.Sprintf(`{"Name":"%s","CallerReference":"%s","Comment":"cli profile updated","EncryptionEntities":{"Quantity":1,"Items":[{"PublicKeyId":"%s","ProviderId":"prov-1","FieldPatterns":{"Quantity":1,"Items":["SSN"]}}]}}`, name, caller, pkID)
	out = runCLI(t, awsCLI("cloudfront", "update-field-level-encryption-profile",
		"--id", id, "--if-match", etag, "--field-level-encryption-profile-config", updCfg, "--output", "json"))
	var upd struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &upd))

	out = runCLI(t, awsCLI("cloudfront", "list-field-level-encryption-profiles", "--output", "json"))
	var list struct {
		FieldLevelEncryptionProfileList struct {
			Items []cfIDItem `json:"Items"`
		} `json:"FieldLevelEncryptionProfileList"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	require.True(t, cfExtras2Contains(list.FieldLevelEncryptionProfileList.Items, id))

	runCLI(t, awsCLI("cloudfront", "delete-field-level-encryption-profile", "--id", id, "--if-match", upd.ETag))
}

// TestCloudFrontKeyValueStoreCLILifecycle covers the key value store CRUD.
func TestCloudFrontKeyValueStoreCLILifecycle(t *testing.T) {
	name := "clikvs" + time.Now().Format("150405000")
	out := runCLI(t, awsCLI("cloudfront", "create-key-value-store",
		"--name", name, "--comment", "cli kvs", "--output", "json"))
	var create struct {
		KeyValueStore struct {
			Name string `json:"Name"`
			ARN  string `json:"ARN"`
		} `json:"KeyValueStore"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	require.Equal(t, name, create.KeyValueStore.Name)
	require.NotEmpty(t, create.KeyValueStore.ARN)
	require.NotEmpty(t, create.ETag)
	defer func() {
		d := runCLIIgnoreErr(awsCLI("cloudfront", "describe-key-value-store", "--name", name, "--output", "json"))
		var dr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(d), &dr) == nil && dr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-key-value-store", "--name", name, "--if-match", dr.ETag))
		}
	}()

	out = runCLI(t, awsCLI("cloudfront", "describe-key-value-store", "--name", name, "--output", "json"))
	var desc struct {
		KeyValueStore struct {
			Comment string `json:"Comment"`
		} `json:"KeyValueStore"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &desc))
	require.Equal(t, "cli kvs", desc.KeyValueStore.Comment)
	descETag := desc.ETag

	out = runCLI(t, awsCLI("cloudfront", "update-key-value-store",
		"--name", name, "--comment", "cli kvs updated", "--if-match", descETag, "--output", "json"))
	var upd struct {
		KeyValueStore struct {
			Comment string `json:"Comment"`
		} `json:"KeyValueStore"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &upd))
	require.Equal(t, "cli kvs updated", upd.KeyValueStore.Comment)

	out = runCLI(t, awsCLI("cloudfront", "list-key-value-stores", "--output", "json"))
	var list struct {
		KeyValueStoreList struct {
			Items []struct {
				Name string `json:"Name"`
			} `json:"Items"`
		} `json:"KeyValueStoreList"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	found := false
	for _, s := range list.KeyValueStoreList.Items {
		if s.Name == name {
			found = true
		}
	}
	require.True(t, found)

	runCLI(t, awsCLI("cloudfront", "delete-key-value-store", "--name", name, "--if-match", upd.ETag))
}

// TestCloudFrontRealtimeLogConfigCLILifecycle covers the real-time log config CRUD.
func TestCloudFrontRealtimeLogConfigCLILifecycle(t *testing.T) {
	name := "clirlc" + time.Now().Format("150405000")
	endpoints := `[{"StreamType":"Kinesis","KinesisStreamConfig":{"RoleARN":"arn:aws:iam::123456789012:role/cf-rt-log","StreamARN":"arn:aws:kinesis:us-east-1:123456789012:stream/cf-rt-log"}}]`
	out := runCLI(t, awsCLI("cloudfront", "create-realtime-log-config",
		"--name", name, "--sampling-rate", "50",
		"--fields", "timestamp", "c-ip",
		"--end-points", endpoints, "--output", "json"))
	var create struct {
		RealtimeLogConfig struct {
			ARN string `json:"ARN"`
		} `json:"RealtimeLogConfig"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	arn := create.RealtimeLogConfig.ARN
	require.NotEmpty(t, arn)
	defer runCLIIgnore(awsCLI("cloudfront", "delete-realtime-log-config", "--name", name))

	out = runCLI(t, awsCLI("cloudfront", "get-realtime-log-config", "--name", name, "--output", "json"))
	var get struct {
		RealtimeLogConfig struct {
			SamplingRate int `json:"SamplingRate"`
		} `json:"RealtimeLogConfig"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &get))
	require.Equal(t, 50, get.RealtimeLogConfig.SamplingRate)

	out = runCLI(t, awsCLI("cloudfront", "update-realtime-log-config",
		"--name", name, "--sampling-rate", "75",
		"--fields", "timestamp", "c-ip", "sc-status",
		"--end-points", endpoints, "--output", "json"))
	var upd struct {
		RealtimeLogConfig struct {
			SamplingRate int `json:"SamplingRate"`
		} `json:"RealtimeLogConfig"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &upd))
	require.Equal(t, 75, upd.RealtimeLogConfig.SamplingRate)

	out = runCLI(t, awsCLI("cloudfront", "list-realtime-log-configs", "--output", "json"))
	var list struct {
		RealtimeLogConfigs struct {
			Items []struct {
				Name string `json:"Name"`
			} `json:"Items"`
		} `json:"RealtimeLogConfigs"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	found := false
	for _, s := range list.RealtimeLogConfigs.Items {
		if s.Name == name {
			found = true
		}
	}
	require.True(t, found)

	runCLI(t, awsCLI("cloudfront", "delete-realtime-log-config", "--arn", arn))
}

// TestCloudFrontStreamingDistributionCLILifecycle covers the streaming distribution CRUD.
func TestCloudFrontStreamingDistributionCLILifecycle(t *testing.T) {
	caller := "cli-sd-" + time.Now().Format("150405.000000")
	cfg := func(comment string) string {
		return fmt.Sprintf(`{"CallerReference":"%s","Comment":"%s","Enabled":true,"S3Origin":{"DomainName":"example.s3.amazonaws.com","OriginAccessIdentity":""},"TrustedSigners":{"Enabled":false,"Quantity":0}}`, caller, comment)
	}
	out := runCLI(t, awsCLI("cloudfront", "create-streaming-distribution",
		"--streaming-distribution-config", cfg("cli streaming"), "--output", "json"))
	var create struct {
		StreamingDistribution struct {
			Id         string `json:"Id"`
			DomainName string `json:"DomainName"`
		} `json:"StreamingDistribution"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.StreamingDistribution.Id
	require.NotEmpty(t, id)
	require.Contains(t, create.StreamingDistribution.DomainName, ".cloudfront.net")
	etag := create.ETag
	defer func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-streaming-distribution", "--id", id, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-streaming-distribution", "--id", id, "--if-match", gr.ETag))
		}
	}()

	runCLI(t, awsCLI("cloudfront", "get-streaming-distribution", "--id", id, "--output", "json"))
	runCLI(t, awsCLI("cloudfront", "get-streaming-distribution-config", "--id", id, "--output", "json"))

	out = runCLI(t, awsCLI("cloudfront", "update-streaming-distribution",
		"--id", id, "--if-match", etag, "--streaming-distribution-config", cfg("cli streaming updated"), "--output", "json"))
	var upd struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &upd))

	out = runCLI(t, awsCLI("cloudfront", "list-streaming-distributions", "--output", "json"))
	var list struct {
		StreamingDistributionList struct {
			Items []cfIDItem `json:"Items"`
		} `json:"StreamingDistributionList"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	require.True(t, cfExtras2Contains(list.StreamingDistributionList.Items, id))

	runCLI(t, awsCLI("cloudfront", "delete-streaming-distribution", "--id", id, "--if-match", upd.ETag))
}

// TestCloudFrontVpcOriginCLILifecycle covers the VPC origin CRUD.
func TestCloudFrontVpcOriginCLILifecycle(t *testing.T) {
	name := "clivo" + time.Now().Format("150405000")
	cfg := fmt.Sprintf(`{"Name":"%s","Arn":"arn:aws:ec2:us-east-1:123456789012:vpc-lattice/service/svc-123","HTTPPort":80,"HTTPSPort":443,"OriginProtocolPolicy":"https-only"}`, name)
	out := runCLI(t, awsCLI("cloudfront", "create-vpc-origin",
		"--vpc-origin-endpoint-config", cfg, "--output", "json"))
	var create struct {
		VpcOrigin struct {
			Id  string `json:"Id"`
			Arn string `json:"Arn"`
		} `json:"VpcOrigin"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.VpcOrigin.Id
	require.NotEmpty(t, id)
	require.NotEmpty(t, create.VpcOrigin.Arn)
	require.NotEmpty(t, create.ETag)
	defer func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-vpc-origin", "--id", id, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-vpc-origin", "--id", id, "--if-match", gr.ETag))
		}
	}()

	out = runCLI(t, awsCLI("cloudfront", "get-vpc-origin", "--id", id, "--output", "json"))
	var get struct {
		VpcOrigin struct {
			VpcOriginEndpointConfig struct {
				Name string `json:"Name"`
			} `json:"VpcOriginEndpointConfig"`
		} `json:"VpcOrigin"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &get))
	require.Equal(t, name, get.VpcOrigin.VpcOriginEndpointConfig.Name)
	getETag := get.ETag

	out = runCLI(t, awsCLI("cloudfront", "update-vpc-origin",
		"--id", id, "--if-match", getETag, "--vpc-origin-endpoint-config", cfg, "--output", "json"))
	var upd struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &upd))

	out = runCLI(t, awsCLI("cloudfront", "list-vpc-origins", "--output", "json"))
	var list struct {
		VpcOriginList struct {
			Items []cfIDItem `json:"Items"`
		} `json:"VpcOriginList"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	require.True(t, cfExtras2Contains(list.VpcOriginList.Items, id))

	// Re-read the ETag since update changed it, then delete.
	g := runCLI(t, awsCLI("cloudfront", "get-vpc-origin", "--id", id, "--output", "json"))
	var gr struct {
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(g), &gr))
	runCLI(t, awsCLI("cloudfront", "delete-vpc-origin", "--id", id, "--if-match", gr.ETag))
}

// TestCloudFrontAnycastIpListCLILifecycle covers the Anycast IP list create/get/list/delete.
func TestCloudFrontAnycastIpListCLILifecycle(t *testing.T) {
	name := "cliail" + time.Now().Format("150405000")
	out := runCLI(t, awsCLI("cloudfront", "create-anycast-ip-list",
		"--name", name, "--ip-count", "3", "--output", "json"))
	var create struct {
		AnycastIpList struct {
			Id      string `json:"Id"`
			IpCount int    `json:"IpCount"`
		} `json:"AnycastIpList"`
		ETag string `json:"ETag"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &create))
	id := create.AnycastIpList.Id
	require.NotEmpty(t, id)
	require.Equal(t, 3, create.AnycastIpList.IpCount)
	etag := create.ETag
	defer func() {
		g := runCLIIgnoreErr(awsCLI("cloudfront", "get-anycast-ip-list", "--id", id, "--output", "json"))
		var gr struct {
			ETag string `json:"ETag"`
		}
		if json.Unmarshal([]byte(g), &gr) == nil && gr.ETag != "" {
			runCLIIgnore(awsCLI("cloudfront", "delete-anycast-ip-list", "--id", id, "--if-match", gr.ETag))
		}
	}()

	out = runCLI(t, awsCLI("cloudfront", "get-anycast-ip-list", "--id", id, "--output", "json"))
	var get struct {
		AnycastIpList struct {
			Name       string   `json:"Name"`
			AnycastIps []string `json:"AnycastIps"`
		} `json:"AnycastIpList"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &get))
	require.Equal(t, name, get.AnycastIpList.Name)
	require.Len(t, get.AnycastIpList.AnycastIps, 3)

	out = runCLI(t, awsCLI("cloudfront", "list-anycast-ip-lists", "--output", "json"))
	var list struct {
		AnycastIpLists struct {
			Items []cfIDItem `json:"Items"`
		} `json:"AnycastIpLists"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &list))
	require.True(t, cfExtras2Contains(list.AnycastIpLists.Items, id))

	runCLI(t, awsCLI("cloudfront", "delete-anycast-ip-list", "--id", id, "--if-match", etag))
}

// cfIDItem is the minimal item shape (an Id field) used by list assertions.
type cfIDItem struct {
	Id string `json:"Id"`
}

// cfExtras2Contains reports whether any item has the given Id.
func cfExtras2Contains(items []cfIDItem, id string) bool {
	for _, it := range items {
		if it.Id == id {
			return true
		}
	}
	return false
}

// runCLIIgnoreErr runs a CLI command, returning its combined output and
// ignoring any error — used in deferred teardown to read a fresh ETag.
func runCLIIgnoreErr(cmd *exec.Cmd) string {
	out, _ := cmd.CombinedOutput()
	return string(out)
}
