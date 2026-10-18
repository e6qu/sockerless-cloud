package nodeagent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to one agent.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// NewClient reaches the agent at base ("http://host:port") with its token.
func NewClient(base, token string) *Client {
	return &Client{base: strings.TrimRight(base, "/"), token: token, http: &http.Client{}}
}

// ErrNotFound is wrapped when the agent knows no guest of the id.
var ErrNotFound = errors.New("the agent knows no such guest")

func (c *Client) do(ctx context.Context, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("node agent %s %s: %w", method, path, err)
	}
	if resp.StatusCode >= 400 {
		defer func() { _ = resp.Body.Close() }()
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var fault struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &fault)
		if fault.Error == "" {
			fault.Error = strings.TrimSpace(string(data))
		}
		if resp.StatusCode == http.StatusNotFound {
			return nil, fmt.Errorf("node agent %s %s: %w: %s", method, path, ErrNotFound, fault.Error)
		}
		return nil, fmt.Errorf("node agent %s %s: HTTP %d: %s", method, path, resp.StatusCode, fault.Error)
	}
	return resp, nil
}

func (c *Client) doJSON(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	contentType := ""
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
		contentType = "application/json"
	}
	resp, err := c.do(ctx, method, path, body, contentType)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Ping reports whether the agent answers.
func (c *Client) Ping(ctx context.Context) error {
	return c.doJSON(ctx, http.MethodGet, "/v1/ping", nil, nil)
}

// Create registers a guest; its root filesystem follows with UploadRootFS.
func (c *Client) Create(ctx context.Context, spec GuestSpec) (GuestState, error) {
	var state GuestState
	err := c.doJSON(ctx, http.MethodPut, "/v1/guests/"+spec.ID, spec, &state)
	return state, err
}

// UploadRootFS streams the guest's root filesystem as a tar archive — an OCI
// image's filesystem as the engine exports it.
func (c *Client) UploadRootFS(ctx context.Context, id string, archive io.Reader) error {
	resp, err := c.do(ctx, http.MethodPut, "/v1/guests/"+id+"/rootfs", archive, "application/x-tar")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// Start boots the guest and returns once its first process answers.
func (c *Client) Start(ctx context.Context, id string) (GuestState, error) {
	var state GuestState
	err := c.doJSON(ctx, http.MethodPost, "/v1/guests/"+id+"/start", nil, &state)
	return state, err
}

// Get reads the guest's state.
func (c *Client) Get(ctx context.Context, id string) (GuestState, error) {
	var state GuestState
	err := c.doJSON(ctx, http.MethodGet, "/v1/guests/"+id, nil, &state)
	return state, err
}

// List reads every guest the agent holds.
func (c *Client) List(ctx context.Context) ([]GuestState, error) {
	var states []GuestState
	err := c.doJSON(ctx, http.MethodGet, "/v1/guests", nil, &states)
	return states, err
}

// Wait blocks until the guest's first process has ended.
func (c *Client) Wait(ctx context.Context, id string) (GuestState, error) {
	var state GuestState
	err := c.doJSON(ctx, http.MethodGet, "/v1/guests/"+id+"/wait", nil, &state)
	return state, err
}

// LogOptions select what Logs reads.
type LogOptions struct {
	// Follow keeps reading new lines until the boot in progress has ended.
	Follow bool
	// CurrentBoot reads the boot in progress alone; otherwise every boot's
	// lines come, the whole console of the machine.
	CurrentBoot bool
}

// Logs hands fn each line the guest's first process wrote.
func (c *Client) Logs(ctx context.Context, id string, opts LogOptions, fn func(LogLine)) error {
	query := url.Values{}
	if opts.Follow {
		query.Set("follow", "1")
	}
	if opts.CurrentBoot {
		query.Set("boot", "current")
	}
	path := "/v1/guests/" + id + "/logs"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	resp, err := c.do(ctx, http.MethodGet, path, nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		var line LogLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		fn(line)
	}
	return scanner.Err()
}

// ExportRootFS streams the guest's root filesystem as a tar archive, which
// the caller closes. A running guest's filesystem is frozen while the agent
// reads it, so the archive is consistent.
func (c *Client) ExportRootFS(ctx context.Context, id string) (io.ReadCloser, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v1/guests/"+id+"/rootfs", nil, "")
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// Exec runs a command in the guest.
func (c *Client) Exec(ctx context.Context, id string, command []string) (ExecResponse, error) {
	var out ExecResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/guests/"+id+"/exec", ExecRequest{Command: command}, &out)
	return out, err
}

// Stop ends the guest's first process within its grace and powers the
// machine off.
func (c *Client) Stop(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/guests/"+id+"/stop", nil, nil)
}

// SetIngress replaces the rules at the guest's tap.
func (c *Client) SetIngress(ctx context.Context, id string, rules []PacketRule) error {
	if rules == nil {
		rules = []PacketRule{}
	}
	return c.doJSON(ctx, http.MethodPut, "/v1/guests/"+id+"/ingress", rules, nil)
}

// AttachDisk gives one of the guest's disk slots a block device of the size.
func (c *Client) AttachDisk(ctx context.Context, id string, slot, sizeMiB int) error {
	return c.doJSON(ctx, http.MethodPut, fmt.Sprintf("/v1/guests/%s/disks/%d", id, slot), map[string]int{"sizeMiB": sizeMiB}, nil)
}

// Delete stops the guest if it runs and removes everything it held.
func (c *Client) Delete(ctx context.Context, id string) error {
	err := c.doJSON(ctx, http.MethodDelete, "/v1/guests/"+id, nil, nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// WaitReady polls Ping until the agent answers or ctx ends.
func (c *Client) WaitReady(ctx context.Context) error {
	for {
		if err := c.Ping(ctx); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("node agent did not answer: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}
