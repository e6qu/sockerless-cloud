package main

import (
	"testing"
)

func TestSecretsManagerRequestConditionKeysReadTheRequestMembers(t *testing.T) {
	resetKMSConditionStores()
	kmsKeys.Put("key-1", KMSKey{KeyId: "key-1", Arn: kmsKeyArn("key-1")})
	kmsAliases.Put("alias/secrets", "key-1")

	cases := []struct {
		name, operation, body string
		want                  map[string][]string
	}{
		{"create", "CreateSecret", `{
			"Name": "db/password", "Description": "orders database", "KmsKeyId": "alias/secrets",
			"Type": "aws-rds", "SecretString": "s",
			"AddReplicaRegions": [{"Region": "eu-west-1"}, {"Region": "ap-south-1", "KmsKeyId": "k"}],
			"ForceOverwriteReplicaSecret": true
		}`, map[string][]string{
			"secretsmanager:Name":                        {"db/password"},
			"secretsmanager:Description":                 {"orders database"},
			"secretsmanager:KmsKeyId":                    {"alias/secrets"},
			"secretsmanager:KmsKeyArn":                   {kmsKeyArn("key-1")},
			"secretsmanager:Type":                        {"aws-rds"},
			"secretsmanager:AddReplicaRegions":           {"eu-west-1", "ap-south-1"},
			"secretsmanager:ForceOverwriteReplicaSecret": {"true"},
		}},
		{"update with an unknown key", "UpdateSecret", `{"SecretId": "s", "Name": "ignored", "KmsKeyId": "missing"}`,
			map[string][]string{"secretsmanager:KmsKeyId": {"missing"}}},
		{"replicate", "ReplicateSecretToRegions", `{"SecretId": "s", "AddReplicaRegions": [{"Region": "us-west-2"}], "ForceOverwriteReplicaSecret": false}`,
			map[string][]string{
				"secretsmanager:AddReplicaRegions":           {"us-west-2"},
				"secretsmanager:ForceOverwriteReplicaSecret": {"false"},
			}},
		{"resource policy", "PutResourcePolicy", `{"SecretId": "s", "ResourcePolicy": "{}", "BlockPublicPolicy": true}`,
			map[string][]string{"secretsmanager:BlockPublicPolicy": {"true"}}},
		{"rotate with rules", "RotateSecret", `{
			"SecretId": "s", "RotationLambdaARN": "arn:aws:lambda:us-east-1:123456789012:function:rotate",
			"ExternalSecretRotationRoleArn": "arn:aws:iam::123456789012:role/rotation",
			"RotationRules": {"AutomaticallyAfterDays": 30}, "RotateImmediately": false
		}`, map[string][]string{
			"secretsmanager:RotationLambdaARN":             {"arn:aws:lambda:us-east-1:123456789012:function:rotate"},
			"secretsmanager:ExternalSecretRotationRoleArn": {"arn:aws:iam::123456789012:role/rotation"},
			"secretsmanager:ModifyRotationRules":           {"true"},
			"secretsmanager:RotateImmediately":             {"false"},
		}},
		{"rotate now", "RotateSecret", `{"SecretId": "s"}`, map[string][]string{
			"secretsmanager:ModifyRotationRules": {"false"},
			"secretsmanager:RotateImmediately":   {"true"},
		}},
		{"delete", "DeleteSecret", `{"SecretId": "s", "RecoveryWindowInDays": 7}`,
			map[string][]string{"secretsmanager:RecoveryWindowInDays": {"7"}}},
		{"force delete", "DeleteSecret", `{"SecretId": "s", "ForceDeleteWithoutRecovery": true}`,
			map[string][]string{"secretsmanager:ForceDeleteWithoutRecovery": {"true"}}},
		{"get version", "GetSecretValue", `{"SecretId": "s", "VersionId": "v-1", "VersionStage": "AWSPREVIOUS"}`,
			map[string][]string{"secretsmanager:VersionId": {"v-1"}, "secretsmanager:VersionStage": {"AWSPREVIOUS"}}},
		{"move stage", "UpdateSecretVersionStage", `{"SecretId": "s", "VersionStage": "AWSCURRENT", "MoveToVersionId": "v-2"}`,
			map[string][]string{"secretsmanager:VersionStage": {"AWSCURRENT"}}},
		{"absent members", "GetSecretValue", `{"SecretId": "s"}`, map[string][]string{}},
		{"undeclared action", "DescribeSecret", `{"SecretId": "s", "VersionId": "v-1", "Name": "n"}`, map[string][]string{}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertConditionValues(t, jsonConditionContext("secretsmanager", c.operation, c.body), c.want)
		})
	}
}
