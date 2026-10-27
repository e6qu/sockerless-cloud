package aws_sdk_test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/require"
)

// iamConformanceVector mirrors the golden corpus
// (../testdata/iam_conformance_vectors.json) the in-process gate also runs.
type iamConformanceVector struct {
	Name        string            `json:"name"`
	Policy      json.RawMessage   `json:"policy"`
	Action      string            `json:"action"`
	Resource    string            `json:"resource"`
	Context     map[string]string `json:"context"`
	ContextType string            `json:"contextType"`
	CallerArn   string            `json:"callerArn"`
	Expect      string            `json:"expect"`
}

// TestIAMConformance_Differential runs the golden corpus through
// SimulateCustomPolicy. Against the simulator it shows the API agrees with the
// in-process evaluator; with SOCKERLESS_IAM_ORACLE=aws the same assertions run
// against AWS with ambient credentials, checking the corpus's expected
// decisions against ground truth. Only the coordinates differ.
func TestIAMConformance_Differential(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile("../testdata/iam_conformance_vectors.json")
	require.NoError(t, err)
	var vectors []iamConformanceVector
	require.NoError(t, json.Unmarshal(data, &vectors))

	c, oracle := iamConformanceClient(t)
	for _, vec := range vectors {
		if vec.CallerArn != "" {
			// SimulateCustomPolicy evaluates identity policies only, so principal and trust
			// policy vectors do not apply.
			continue
		}
		t.Run(vec.Name, func(t *testing.T) {
			in := &iam.SimulateCustomPolicyInput{
				PolicyInputList: []string{string(vec.Policy)},
				ActionNames:     []string{vec.Action},
				ResourceArns:    []string{vec.Resource},
			}
			for k, v := range vec.Context {
				in.ContextEntries = append(in.ContextEntries, iamtypes.ContextEntry{
					ContextKeyName:   aws.String(k),
					ContextKeyValues: []string{v},
					ContextKeyType:   iamContextKeyType(vec.ContextType),
				})
			}
			out, err := c.SimulateCustomPolicy(ctx, in)
			require.NoError(t, err)
			require.Len(t, out.EvaluationResults, 1)
			got := string(out.EvaluationResults[0].EvalDecision)
			if got != vec.Expect {
				t.Fatalf("%s: %s decision = %q, want %q", vec.Name, oracle, got, vec.Expect)
			}
		})
	}
}

func iamContextKeyType(t string) iamtypes.ContextKeyTypeEnum {
	switch t {
	case "numeric":
		return iamtypes.ContextKeyTypeEnumNumeric
	case "ip":
		return iamtypes.ContextKeyTypeEnumIp
	case "date":
		return iamtypes.ContextKeyTypeEnumDate
	case "boolean":
		return iamtypes.ContextKeyTypeEnumBoolean
	default:
		return iamtypes.ContextKeyTypeEnumString
	}
}

// iamConformanceClient returns the IAM client and the oracle's label: the
// simulator, or AWS with SOCKERLESS_IAM_ORACLE=aws.
func iamConformanceClient(t *testing.T) (*iam.Client, string) {
	if os.Getenv("SOCKERLESS_IAM_ORACLE") != "aws" {
		return iamClient(), "sim"
	}
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatalf("SOCKERLESS_IAM_ORACLE=aws but no AWS credentials: %v", err)
	}
	if _, err := cfg.Credentials.Retrieve(ctx); err != nil {
		t.Fatalf("SOCKERLESS_IAM_ORACLE=aws but credentials don't resolve: %v", err)
	}
	return iam.NewFromConfig(cfg), "real-aws"
}
