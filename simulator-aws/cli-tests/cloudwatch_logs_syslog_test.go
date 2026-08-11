package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudWatchLogsStorageTierAndSyslogCLI(t *testing.T) {
	group := "/cli/syslog"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	runCLI(t, awsCLI("logs", "put-storage-tier-policy", "--storage-tier", "INTELLIGENT_TIERING"))
	policyOut := runCLI(t, awsCLI("logs", "get-storage-tier-policy", "--output", "json"))
	var policy struct {
		StorageTier     string `json:"storageTier"`
		LastUpdatedTime int64  `json:"lastUpdatedTime"`
	}
	parseJSON(t, policyOut, &policy)
	assert.Equal(t, "INTELLIGENT_TIERING", policy.StorageTier)
	require.NotZero(t, policy.LastUpdatedTime)

	runCLI(t, awsCLI("logs", "put-syslog-configuration", "--log-group-identifier", group))
	listOut := runCLI(t, awsCLI("logs", "list-syslog-configurations",
		"--log-group-identifier", group, "--output", "json"))
	var listed struct {
		SyslogConfigurations []struct {
			LogGroupArn string `json:"logGroupArn"`
			SourceType  string `json:"sourceType"`
		} `json:"syslogConfigurations"`
	}
	parseJSON(t, listOut, &listed)
	require.Len(t, listed.SyslogConfigurations, 1)
	assert.Equal(t, "VPCE", listed.SyslogConfigurations[0].SourceType)

	runCLI(t, awsCLI("logs", "delete-syslog-configuration", "--log-group-identifier", group))
}
