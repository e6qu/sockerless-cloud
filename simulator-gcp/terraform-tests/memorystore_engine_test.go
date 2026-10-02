package gcp_tf_test

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tlsRedisCommand dials address over TLS, trusting only caPEM, and returns
// the reply to one command: its first line, or the text of a bulk reply.
func tlsRedisCommand(t *testing.T, address, caPEM string, args ...string) string {
	t.Helper()
	roots := x509.NewCertPool()
	require.True(t, roots.AppendCertsFromPEM([]byte(caPEM)), "the reported server CA is PEM")
	conn, err := tls.Dial("tcp", address, &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12})
	require.NoError(t, err, "%s presents a certificate the reported server CA signed", address)
	defer conn.Close()
	require.NoError(t, conn.SetDeadline(time.Now().Add(10*time.Second)))
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, arg := range args {
		b.WriteString("$" + strconv.Itoa(len(arg)) + "\r\n" + arg + "\r\n")
	}
	_, err = conn.Write([]byte(b.String()))
	require.NoError(t, err)
	reader := bufio.NewReader(conn)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "$") {
		return line
	}
	size, err := strconv.Atoi(line[1:])
	require.NoError(t, err)
	data := make([]byte, size+2)
	_, err = io.ReadFull(reader, data)
	require.NoError(t, err)
	return string(data[:size])
}

// A google_redis_instance and a google_redis_cluster that encrypt in transit
// serve TLS from the CAs the provider reads back, and a replica_count or
// shard_count change reshapes the running engine.
func TestTerraformMemorystoreEngine(t *testing.T) {
	fixtureDir := filepath.Join("fixtures", "memorystore-engine")
	cleanTerraformFixture(t, fixtureDir)
	vars := func(replicas, shards int) []string {
		return []string{"-var", "replica_count=" + strconv.Itoa(replicas), "-var", "shard_count=" + strconv.Itoa(shards)}
	}

	out, err := runTimed(t, "terraform init", terraformCmdInDir(fixtureDir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	out, err = runTimed(t, "terraform apply", terraformCmdInDir(fixtureDir, append([]string{"apply", "-auto-approve"}, vars(1, 1)...)...))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy", terraformCmdInDir(fixtureDir, append([]string{"destroy", "-auto-approve"}, vars(2, 2)...)...))
		assert.NoError(t, err, "terraform destroy failed:\n%s", out)
	})

	outputs := readOutputsInDir(t, fixtureDir)
	instance := net.JoinHostPort(outputs.must(t, "instance_host"), strconv.Itoa(int(outputs.mustNumber(t, "instance_port"))))
	assert.Equal(t, float64(6378), outputs.mustNumber(t, "instance_port"))
	assert.Equal(t, "RDB", outputs.must(t, "instance_persistence_mode"))
	assert.Equal(t, "AOF", outputs.must(t, "cluster_persistence_mode"))
	instanceCA, auth := outputs.must(t, "instance_server_ca"), outputs.must(t, "instance_auth_string")
	assert.Equal(t, "+OK", tlsRedisCommand(t, instance, instanceCA, "AUTH", auth))
	assert.Contains(t, tlsRedisCommand(t, instance, instanceCA, "PING"), "NOAUTH", "the instance requires its AUTH string")

	discovery := net.JoinHostPort(outputs.must(t, "cluster_discovery_address"), strconv.Itoa(int(outputs.mustNumber(t, "cluster_discovery_port"))))
	clusterCA := outputs.must(t, "cluster_server_ca")
	assert.Contains(t, tlsRedisCommand(t, discovery, clusterCA, "CLUSTER", "INFO"), "cluster_size:1")

	out, err = runTimed(t, "terraform apply (reshape)", terraformCmdInDir(fixtureDir, append([]string{"apply", "-auto-approve"}, vars(2, 2)...)...))
	require.NoError(t, err, "terraform apply with a new replica_count and shard_count failed:\n%s", out)
	outputs = readOutputsInDir(t, fixtureDir)
	assert.Equal(t, float64(2), outputs.mustNumber(t, "instance_replica_count"))
	assert.Equal(t, float64(2), outputs.mustNumber(t, "cluster_shard_count"))
	assert.Contains(t, tlsRedisCommand(t, discovery, clusterCA, "CLUSTER", "INFO"), "cluster_size:2", "both shards serve slots")

	out, err = runTimed(t, "terraform plan", terraformCmdInDir(fixtureDir, append([]string{"plan", "-detailed-exitcode"}, vars(2, 2)...)...))
	require.NoError(t, err, "the applied configuration plans no change:\n%s", out)
}
