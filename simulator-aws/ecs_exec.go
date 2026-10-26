package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/gorilla/websocket"
	"github.com/moby/moby/api/pkg/stdcopy"
	dockerclient "github.com/moby/moby/client"
)

var ecsExecSessions sync.Map // map[sessionID]ecsExecSession

type ecsExecSession struct {
	taskID            string
	command           string
	dockerContainerID string
	tokenValue        string
}

// Each chunk must be an SSM output_stream_data AgentMessage frame; clients
// decode raw bytes as empty output.
type ssmStreamWriter struct {
	conn        *websocket.Conn
	payloadType uint32 // 1 = stdout, 11 = stderr
	mu          *sync.Mutex
}

func (w *ssmStreamWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	frame := buildSSMOutputFrame(w.payloadType, p)
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return 0, err
	}
	return len(p), nil
}

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// Fetch each valueFrom from Secrets Manager or Parameter Store at task launch,
// as Amazon ECS does.
func resolveECSContainerSecrets(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	if len(raw) == 0 {
		return out
	}
	var secrets []struct {
		Name      string `json:"name"`
		ValueFrom string `json:"valueFrom"`
	}
	if err := json.Unmarshal(raw, &secrets); err != nil {
		return out
	}
	for _, s := range secrets {
		if s.Name == "" || s.ValueFrom == "" {
			continue
		}
		if v, ok := resolveECSSecretValue(s.ValueFrom); ok {
			out[s.Name] = v
		}
	}
	return out
}

// Secrets Manager: arn:aws:secretsmanager:…:secret:name-suffix[:jsonKey:stage:id],
// where jsonKey selects a field of a JSON SecretString. Parameter Store:
// arn:aws:ssm:…:parameter/name or a bare /name.
func resolveECSSecretValue(valueFrom string) (string, bool) {
	if strings.Contains(valueFrom, ":secretsmanager:") {
		parts := strings.Split(valueFrom, ":")
		if len(parts) < 7 {
			return "", false
		}
		baseARN := strings.Join(parts[:7], ":")
		secret, ok := resolveSMSecret(baseARN)
		if !ok {
			return "", false
		}
		val := secret.SecretString
		if len(parts) >= 8 && parts[7] != "" {
			var m map[string]any
			if json.Unmarshal([]byte(val), &m) == nil {
				if jv, ok := m[parts[7]]; ok {
					return fmt.Sprint(jv), true
				}
			}
		}
		return val, true
	}
	name := valueFrom
	if i := strings.Index(valueFrom, ":parameter"); i >= 0 {
		name = valueFrom[i+len(":parameter"):]
	}
	if p, ok := ssmParams.Get(ensureLeadingSlash(name)); ok {
		return p.Value, true
	}
	return "", false
}

func handleECSExecuteCommand(srv *sim.Server) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Cluster     string `json:"cluster"`
			Task        string `json:"task"`
			Container   string `json:"container"`
			Command     string `json:"command"`
			Interactive bool   `json:"interactive"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AWSError(w, "InvalidParameterException", "Invalid request body", http.StatusBadRequest)
			return
		}
		if req.Task == "" {
			AWSError(w, "InvalidParameterException", "task is required", http.StatusBadRequest)
			return
		}
		if req.Command == "" {
			AWSError(w, "InvalidParameterException", "command is required", http.StatusBadRequest)
			return
		}
		if !req.Interactive {
			AWSError(w, "InvalidParameterException",
				"Amazon ECS only supports initiating interactive execute command sessions. Specify interactive as true.",
				http.StatusBadRequest)
			return
		}

		taskID := req.Task
		if strings.HasPrefix(taskID, "arn:") {
			parts := strings.Split(taskID, "/")
			if len(parts) > 0 {
				taskID = parts[len(parts)-1]
			}
		}

		task, ok := ecsTasks.Get(taskID)
		if !ok {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"Task not found: %s", req.Task)
			return
		}
		if task.LastStatus != ECSTaskStatusRunning {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"Execute command is not supported on task in %s status", task.LastStatus)
			return
		}
		if req.Cluster != "" && task.ClusterArn != ecsArn("cluster", ecsClusterNameFromRef(req.Cluster)) {
			AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
				"Task not found: %s", req.Task)
			return
		}
		container := ecsExecTargetContainer(task, req.Container)
		if container == nil {
			if req.Container == "" {
				AWSError(w, "InvalidParameterException",
					"container is required when the task has multiple containers",
					http.StatusBadRequest)
			} else {
				AWSErrorf(w, "InvalidParameterException", http.StatusBadRequest,
					"Container not found: %s", req.Container)
			}
			return
		}
		// Exec requires enableExecuteCommand=true, which injects the SSM agent.
		if !task.EnableExecuteCommand {
			AWSError(w, "InvalidParameterException",
				"The execute command failed because execute command was not enabled when the task was run or the execute command agent isn't running. Wait and try again or run a new task with execute command enabled and try again.",
				http.StatusBadRequest)
			return
		}

		sessionID := sim.NewUUID()

		// A task reports RUNNING only once its container handles are stored.
		var dockerContainerID string
		if v, ok := ecsProcessHandles.Load(taskID); ok {
			if procs, ok := v.(*ecsTaskProcesses); ok {
				if handle := procs.handleFor(container.Name); handle != nil {
					dockerContainerID = handle.ID()
				}
			}
		}
		if dockerContainerID == "" {
			AWSError(w, "TargetNotConnectedException",
				"The execute command cannot run because the task target is not connected.",
				http.StatusBadRequest)
			return
		}

		tokenValue := "token-" + sessionID[:8]
		ecsExecSessions.Store(sessionID, ecsExecSession{
			taskID:            taskID,
			command:           req.Command,
			dockerContainerID: dockerContainerID,
			tokenValue:        tokenValue,
		})

		host := r.Host
		if host == "" {
			host = "localhost:4566"
		}
		streamURL := fmt.Sprintf("ws://%s/ecs-exec/%s", host, sessionID)

		// /ecs-exec/{sessionId} is registered statically.

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"clusterArn":    task.ClusterArn,
			"containerArn":  container.ContainerArn,
			"containerName": container.Name,
			"interactive":   true,
			"session": map[string]any{
				"sessionId":  sessionID,
				"streamUrl":  streamURL,
				"tokenValue": tokenValue,
			},
			"taskArn": task.TaskArn,
		})
	}
}

func ecsExecTargetContainer(task ECSTask, containerName string) *ECSTaskContainer {
	if containerName != "" {
		for i := range task.Containers {
			if task.Containers[i].Name == containerName {
				return &task.Containers[i]
			}
		}
		return nil
	}
	if len(task.Containers) == 1 {
		return &task.Containers[0]
	}
	return nil
}

func handleECSExecWebSocket(sessionID string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessVal, ok := ecsExecSessions.LoadAndDelete(sessionID)
		if !ok {
			http.Error(w, "session not found or already used", http.StatusNotFound)
			return
		}
		sess, ok := sessVal.(ecsExecSession)
		if !ok {
			http.Error(w, "session not found or already used", http.StatusNotFound)
			return
		}

		conn, err := wsUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck

		// A Session Manager client sends OpenDataChannel, carrying the
		// ExecuteCommand TokenValue, as the first frame; validate it before any
		// AgentMessage streaming.
		if !validateSSMOpenDataChannel(conn, sess.tokenValue) {
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation,
					"OpenDataChannel handshake failed: invalid or missing TokenValue"))
			return
		}

		if sess.dockerContainerID != "" {
			cli := sim.DockerClient()
			if cli != nil {
				// Always run the received string as one shell script: Amazon ECS exec()s
				// argv[0], so clients wrap commands in `sh -c '…'` themselves, and wrapping
				// again is harmless.
				execCmd := []string{"sh", "-c", sess.command}
				execCfg := dockerclient.ExecCreateOptions{
					Cmd:          execCmd,
					AttachStdin:  true,
					AttachStdout: true,
					AttachStderr: true,
				}
				execResp, err := cli.ExecCreate(r.Context(), sess.dockerContainerID, execCfg)
				if err != nil {
					_ = conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
					return
				}
				attach, err := cli.ExecAttach(r.Context(), execResp.ID, dockerclient.ExecAttachOptions{})
				if err != nil {
					_ = conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
					return
				}
				defer attach.Close()

				// Forward only frame payloads to stdin and close stdin on the FIN flag, as
				// ssm-agent does, so `cat`, `tar` and `gzip` see EOF.
				bg.Go(func() {
					defer attach.CloseWrite() //nolint:errcheck
					for {
						_, msg, rerr := conn.ReadMessage()
						if rerr != nil {
							return
						}
						payload, mt, fin, perr := decodeSSMInputFrame(msg)
						if perr != nil {
							// ssm-agent ignores unrecognized frames.
							continue
						}
						if mt != ssmMTInputStreamData {
							continue
						}
						if len(payload) > 0 {
							if _, werr := attach.Conn.Write(payload); werr != nil {
								return
							}
						}
						if fin {
							return
						}
					}
				})

				// Clients see output only as output_stream_data frames.
				writeMu := &sync.Mutex{}
				stdoutWriter := &ssmStreamWriter{conn: conn, payloadType: ssmPayloadStdout, mu: writeMu}
				stderrWriter := &ssmStreamWriter{conn: conn, payloadType: ssmPayloadStderr, mu: writeMu}
				_, _ = stdcopy.StdCopy(stdoutWriter, stderrWriter, attach.Reader)

				// Session Manager sends the exit code in a PayloadType=12 frame before closing
				// the channel.
				exitCode := 0
				if inspect, err := cli.ExecInspect(r.Context(), execResp.ID, dockerclient.ExecInspectOptions{}); err == nil {
					exitCode = inspect.ExitCode
				}
				writeMu.Lock()
				_ = conn.WriteMessage(websocket.BinaryMessage,
					buildSSMOutputFrame(ssmPayloadExitCode, []byte(strconv.Itoa(exitCode))))
				_ = conn.WriteMessage(websocket.BinaryMessage, buildSSMChannelClosed())
				writeMu.Unlock()

				_ = conn.WriteControl(
					websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
					time.Now().Add(5*time.Second),
				)
				return
			}
		}

		// Never run the command on the simulator host: the task's container is the
		// host.
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr,
				"ECS ExecuteCommand requires a running Docker container for the task"))
	}
}

// Mirrors session-manager-plugin's OpenDataChannelInput; only TokenValue
// matters here.
type ssmOpenDataChannelInput struct {
	MessageSchemaVersion string `json:"MessageSchemaVersion"`
	RequestId            string `json:"RequestId"`
	TokenValue           string `json:"TokenValue"`
	ClientId             string `json:"ClientId"`
}

// A short deadline bounds a client that never sends the handshake.
func validateSSMOpenDataChannel(conn *websocket.Conn, expectedToken string) bool {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return false
	}
	_ = conn.SetReadDeadline(time.Time{})

	var in ssmOpenDataChannelInput
	if err := json.Unmarshal(msg, &in); err != nil {
		return false
	}
	return in.TokenValue != "" && in.TokenValue == expectedToken
}
