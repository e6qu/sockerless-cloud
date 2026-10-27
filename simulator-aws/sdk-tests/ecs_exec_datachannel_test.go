package aws_sdk_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dialExecDataChannel runs an exec-enabled task, calls ExecuteCommand and dials
// the returned streamUrl, returning the WebSocket and the session's tokenValue
// for the caller to send OpenDataChannel as session-manager-plugin does.
func dialExecDataChannel(t *testing.T, cluster, command string) (*websocket.Conn, string) {
	t.Helper()
	return dialExecDataChannelIn(t, cluster, command, "")
}

// dialExecDataChannelIn is dialExecDataChannel for a task whose container
// definition names a workingDirectory.
func dialExecDataChannelIn(t *testing.T, cluster, command, workingDirectory string) (*websocket.Conn, string) {
	t.Helper()
	client := ecsClient()
	taskArn := runLongLivedECSTaskIn(t, client, cluster, cluster, true, workingDirectory)

	out, err := client.ExecuteCommand(ctx, &ecs.ExecuteCommandInput{
		Cluster:     aws.String(cluster),
		Task:        aws.String(taskArn),
		Container:   aws.String("app"),
		Command:     aws.String(command),
		Interactive: true,
	})
	require.NoError(t, err)
	require.NotNil(t, out.Session)
	streamURL := aws.ToString(out.Session.StreamUrl)
	tokenValue := aws.ToString(out.Session.TokenValue)
	require.NotEmpty(t, streamURL)
	require.NotEmpty(t, tokenValue)

	conn, _, err := websocket.DefaultDialer.Dial(streamURL, nil)
	require.NoError(t, err, "dial SSM streamUrl")
	t.Cleanup(func() { _ = conn.Close() })
	return conn, tokenValue
}

// sendOpenDataChannel writes the OpenDataChannel handshake, the JSON document a
// Session Manager client sends as the first WebSocket frame, carrying the
// TokenValue ExecuteCommand returned.
func sendOpenDataChannel(t *testing.T, conn *websocket.Conn, token string) {
	t.Helper()
	handshake, _ := json.Marshal(map[string]string{
		"MessageSchemaVersion": "1.0",
		"RequestId":            uuid.New().String(),
		"TokenValue":           token,
		"ClientId":             uuid.New().String(),
	})
	require.NoError(t, conn.WriteMessage(websocket.TextMessage, handshake))
}

// TestECS_ExecDataChannelHandshake opens the data channel with the session's
// TokenValue, as a real client does: the correct token streams the exec output
// ("hello") back, and a wrong one is rejected.
func TestECS_ExecDataChannelHandshake(t *testing.T) {
	t.Parallel()
	conn, token := dialExecDataChannel(t, "exec-datachannel-ok", "echo hello")
	sendOpenDataChannel(t, conn, token)

	// The exec's stdout streams back as output_stream_data AgentMessage frames.
	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	gotHello := false
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if bytes.Contains(msg, []byte("hello")) {
			gotHello = true
			break
		}
	}
	assert.True(t, gotHello, "exec output must stream after a valid OpenDataChannel handshake")
}

// TestECS_ExecRunsInTaskWorkingDirectory: Amazon ECS starts the container's
// process in the definition's workingDirectory, creating it when the image
// lacks it, and an ExecuteCommand session inherits it, so `pwd` reports it.
func TestECS_ExecRunsInTaskWorkingDirectory(t *testing.T) {
	t.Parallel()
	const workDir = "/srv/sockerless-work"
	conn, token := dialExecDataChannelIn(t, "exec-datachannel-workdir", "pwd", workDir)
	sendOpenDataChannel(t, conn, token)

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	gotDir := false
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if bytes.Contains(msg, []byte(workDir)) {
			gotDir = true
			break
		}
	}
	assert.True(t, gotDir, "exec must run in the task definition's workingDirectory")
}

// TestECS_ExecDataChannelRejectsBadToken expects a data channel opened with the
// wrong TokenValue to close without streaming: AWS validates the token before
// any AgentMessage flows.
func TestECS_ExecDataChannelRejectsBadToken(t *testing.T) {
	t.Parallel()
	conn, _ := dialExecDataChannel(t, "exec-datachannel-badtoken", "echo hello")
	sendOpenDataChannel(t, conn, "token-bogus-00000000")

	_ = conn.SetReadDeadline(time.Now().Add(15 * time.Second))
	gotHello := false
	closed := false
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			closed = true
			break
		}
		if bytes.Contains(msg, []byte("hello")) {
			gotHello = true
			break
		}
	}
	assert.False(t, gotHello, "no exec output may stream when the OpenDataChannel token is invalid")
	assert.True(t, closed, "the data channel must close on an invalid token")
}
