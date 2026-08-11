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

// dialExecDataChannel runs a long-lived exec-enabled task, issues
// ExecuteCommand, and dials the returned SSM streamUrl. It returns the open
// WebSocket connection plus the session's tokenValue so the caller drives the
// OpenDataChannel handshake exactly as a real client (session-manager-plugin)
// does against real AWS.
func dialExecDataChannel(t *testing.T, cluster, command string) (*websocket.Conn, string) {
	t.Helper()
	client := ecsClient()
	taskArn := runLongLivedECSTask(t, client, cluster, cluster, true)

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

// sendOpenDataChannel writes the OpenDataChannel handshake — the JSON document
// a real SSM Session Manager client sends as the FIRST WebSocket frame to open
// the data channel, carrying the TokenValue ExecuteCommand returned.
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

// TestECS_ExecDataChannelHandshake covers the coordinate-only consumer path:
// after ExecuteCommand, a real client opens the data channel by sending an
// OpenDataChannel message with the session's TokenValue, then streams. With
// the correct token the exec output ("hello") streams back; with a wrong token
// the service rejects the channel. The sim must behave identically to real AWS
// so one client works against both.
func TestECS_ExecDataChannelHandshake(t *testing.T) {
	conn, token := dialExecDataChannel(t, "exec-datachannel-ok", "echo hello")
	sendOpenDataChannel(t, conn, token)

	// After a valid handshake, the exec runs and its stdout streams back as
	// SSM output_stream_data AgentMessage frames; the payload carries "hello".
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

// TestECS_ExecDataChannelRejectsBadToken covers token validation: a client
// that opens the data channel with the wrong TokenValue is rejected (the
// channel closes without streaming) — matching real AWS, which validates the
// token before any AgentMessage flows.
func TestECS_ExecDataChannelRejectsBadToken(t *testing.T) {
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
