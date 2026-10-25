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

// ecsExecSessions tracks active ECS exec sessions for WebSocket handlers.
var ecsExecSessions sync.Map // map[sessionID]ecsExecSession

type ecsExecSession struct {
	taskID            string
	command           string
	dockerContainerID string
	tokenValue        string
}

// ssmStreamWriter wraps chunks in an SSM output_stream_data AgentMessage
// frame before sending over the WebSocket. The backend's decoder
// parses these frames to reconstruct the Docker-mux'd stream;
// sending raw bytes silently produces empty exec output.
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

// resolveECSContainerSecrets resolves a container definition's `secrets` array
// (`[{"name","valueFrom"}]`) to name→value pairs by fetching each `valueFrom`
// from Secrets Manager or SSM Parameter Store, as real ECS does at task launch.
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

// resolveECSSecretValue resolves a single ECS secret `valueFrom` reference.
// Secrets Manager: arn:aws:secretsmanager:…:secret:name-suffix[:jsonKey:stage:id]
// — an optional jsonKey selects a field from a JSON SecretString. SSM:
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
	// SSM Parameter Store (ARN or bare name).
	name := valueFrom
	if i := strings.Index(valueFrom, ":parameter"); i >= 0 {
		name = valueFrom[i+len(":parameter"):]
	}
	if p, ok := ssmParams.Get(ensureLeadingSlash(name)); ok {
		return p.Value, true
	}
	return "", false
}

// handleECSExecuteCommand returns a handler that implements ECS ExecuteCommand.
// It creates a session and registers a WebSocket handler for command execution.
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

		// Extract task ID from ARN
		taskID := req.Task
		if strings.HasPrefix(taskID, "arn:") {
			parts := strings.Split(taskID, "/")
			if len(parts) > 0 {
				taskID = parts[len(parts)-1]
			}
		}

		// Verify task exists and is RUNNING
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
		// Real ECS rejects exec unless the task was started with
		// enableExecuteCommand=true (the SSM exec agent is only injected then).
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

		// Determine host from the incoming request
		host := r.Host
		if host == "" {
			host = "localhost:4566"
		}
		streamURL := fmt.Sprintf("ws://%s/ecs-exec/%s", host, sessionID)

		// WebSocket endpoint is registered statically as /ecs-exec/{sessionId}

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

// handleECSExecWebSocket returns a handler for an ECS exec WebSocket session.
// It upgrades the connection and bridges stdin/stdout/stderr of the command.
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

		// SSM Session Manager data-channel handshake. A real client
		// (session-manager-plugin, or any faithful client like the ECS
		// backend's exec_cloud.go) sends an OpenDataChannel message as the
		// FIRST WebSocket frame — a JSON document carrying the TokenValue
		// returned by ECS.ExecuteCommand. The service validates the token
		// before any AgentMessage streaming begins. Consume and validate it
		// here so a coordinate-only consumer drives the sim exactly as it
		// drives real AWS; without this the token is never checked and the
		// first frame would be mis-read as input_stream_data.
		if !validateSSMOpenDataChannel(conn, sess.tokenValue) {
			_ = conn.WriteMessage(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.ClosePolicyViolation,
					"OpenDataChannel handshake failed: invalid or missing TokenValue"))
			return
		}

		// Execute command inside the real Docker container
		if sess.dockerContainerID != "" {
			cli := sim.DockerClient()
			if cli != nil {
				// Always wrap the entire received string as a shell script.
				// Backends now wrap commands in `sh -c '<script>'` before
				// sending to ECS.ExecuteCommand (real AWS exec()s argv[0]
				// and rejects shell builtins / pipes / env-expansion). The
				// previous "unwrap if it starts with sh -c " path stripped
				// `-c ` then handed the remaining bytes to Docker exec
				// verbatim, which left the surrounding single quotes
				// intact — `'echo …'` was then exec()'d as a single
				// command name and produced "sh: 'echo …': not found".
				// Treat the whole received string as one shell script
				// regardless of whether the backend already wrapped it;
				// double-wrapping is correct (the inner shell parses the
				// outer script and dispatches the inner shell itself).
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

				// Bridge: WebSocket → Docker exec stdin. The backend wraps
				// stdin in SSM `input_stream_data` AgentMessage frames; real
				// ssm-agent decodes the frame, forwards only the payload to
				// the user process, and closes the user's stdin when the
				// frame's FIN flag is set so readers like `cat`, `tar`, and
				// `gzip` see EOF. Match that contract.
				bg.Go(func() {
					defer attach.CloseWrite() //nolint:errcheck
					for {
						_, msg, rerr := conn.ReadMessage()
						if rerr != nil {
							return
						}
						payload, mt, fin, perr := decodeSSMInputFrame(msg)
						if perr != nil {
							// Not a parseable SSM frame — skip silently.
							// Real ssm-agent ignores unrecognized frames.
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

				// Bridge: Docker exec → WebSocket wrapped in SSM
				// AgentMessage frames. The backend's SSM decoder
				// (backends/ecs/exec_cloud.go, will only see
				// output if each chunk arrives as a proper
				// output_stream_data frame.
				writeMu := &sync.Mutex{}
				stdoutWriter := &ssmStreamWriter{conn: conn, payloadType: ssmPayloadStdout, mu: writeMu}
				stderrWriter := &ssmStreamWriter{conn: conn, payloadType: ssmPayloadStderr, mu: writeMu}
				_, _ = stdcopy.StdCopy(stdoutWriter, stderrWriter, attach.Reader)

				// Real AWS Session Manager sends an output_stream_data
				// frame with PayloadType=12 carrying the exec process's
				// exit code before the channel is closed. Match that so
				// the backend decoder sees the true exit status.
				exitCode := 0
				if inspect, err := cli.ExecInspect(r.Context(), execResp.ID, dockerclient.ExecInspectOptions{}); err == nil {
					exitCode = inspect.ExitCode
				}
				writeMu.Lock()
				_ = conn.WriteMessage(websocket.BinaryMessage,
					buildSSMOutputFrame(ssmPayloadExitCode, []byte(strconv.Itoa(exitCode))))
				// Then signal channel close so the decoder unwinds cleanly.
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

		// No fallback: ExecuteCommand requires the task's Docker
		// container. The sim never `os/exec`s the command on the sim
		// host — that would run against the wrong "host" entirely
		// (sim-binary host, not the Fargate-shaped task container).
		// See feedback_sim_host_model.md.
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr,
				"ECS ExecuteCommand requires a running Docker container for the task"))
	}
}

// ssmOpenDataChannelInput is the JSON body a client sends as the first
// WebSocket frame to open an SSM Session Manager data channel. Mirrors
// the session-manager-plugin's service.OpenDataChannelInput; only
// TokenValue is load-bearing for the sim (it validates the token issued
// by ECS.ExecuteCommand). Extra fields are tolerated.
type ssmOpenDataChannelInput struct {
	MessageSchemaVersion string `json:"MessageSchemaVersion"`
	RequestId            string `json:"RequestId"`
	TokenValue           string `json:"TokenValue"`
	ClientId             string `json:"ClientId"`
}

// validateSSMOpenDataChannel reads the first WebSocket frame, parses it as
// an OpenDataChannel message, and reports whether its TokenValue matches
// the token issued for this session. A short read deadline bounds a client
// that connects but never sends the handshake (real AWS times such
// connections out rather than streaming).
func validateSSMOpenDataChannel(conn *websocket.Conn, expectedToken string) bool {
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		return false
	}
	// Clear the deadline so subsequent stdin frames aren't bounded.
	_ = conn.SetReadDeadline(time.Time{})

	var in ssmOpenDataChannelInput
	if err := json.Unmarshal(msg, &in); err != nil {
		return false
	}
	return in.TokenValue != "" && in.TokenValue == expectedToken
}
