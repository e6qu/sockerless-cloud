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
	"net/netip"
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

// Wait blocks until the guest's first process has ended; the machine runs on
// while members do.
func (c *Client) Wait(ctx context.Context, id string) (GuestState, error) {
	var state GuestState
	err := c.doJSON(ctx, http.MethodGet, "/v1/guests/"+id+"/wait", nil, &state)
	return state, err
}

// WaitStopped blocks until the machine has stopped: its first process and
// every member have ended.
func (c *Client) WaitStopped(ctx context.Context, id string) (GuestState, error) {
	var state GuestState
	err := c.doJSON(ctx, http.MethodGet, "/v1/guests/"+id+"/wait?machine=1", nil, &state)
	return state, err
}

// CreateMember declares a member of a running guest; its root filesystem
// follows with UploadMemberRootFS and it runs from StartMember.
func (c *Client) CreateMember(ctx context.Context, id string, spec MemberSpec) (MemberState, error) {
	var state MemberState
	err := c.doJSON(ctx, http.MethodPut, "/v1/guests/"+id+"/members/"+spec.Name, spec, &state)
	return state, err
}

// UploadMemberRootFS streams the member's root filesystem as a tar archive.
func (c *Client) UploadMemberRootFS(ctx context.Context, id, name string, archive io.Reader) error {
	resp, err := c.do(ctx, http.MethodPut, "/v1/guests/"+id+"/members/"+name+"/rootfs", archive, "application/x-tar")
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}

// StartMember mounts the member's root filesystem in the guest and starts
// its process.
func (c *Client) StartMember(ctx context.Context, id, name string) (MemberState, error) {
	var state MemberState
	err := c.doJSON(ctx, http.MethodPost, "/v1/guests/"+id+"/members/"+name+"/start", nil, &state)
	return state, err
}

// WaitMember blocks until the member's process has ended.
func (c *Client) WaitMember(ctx context.Context, id, name string) (MemberState, error) {
	var state MemberState
	err := c.doJSON(ctx, http.MethodGet, "/v1/guests/"+id+"/members/"+name+"/wait", nil, &state)
	return state, err
}

// StopMember ends the member's process within its grace.
func (c *Client) StopMember(ctx context.Context, id, name string) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/guests/"+id+"/members/"+name+"/stop", nil, nil)
}

// LogOptions select what Logs reads.
type LogOptions struct {
	// Follow keeps reading new lines until the boot in progress has ended,
	// or, with Process set, until that process has ended and every line it
	// wrote is out.
	Follow bool
	// CurrentBoot reads the boot in progress alone; otherwise every boot's
	// lines come, the whole console of the machine.
	CurrentBoot bool
	// Process reads one process's lines: InitProcess for the first process,
	// a member's name for the member; empty reads every process's.
	Process string
}

// Logs hands fn each line the guest's processes wrote.
func (c *Client) Logs(ctx context.Context, id string, opts LogOptions, fn func(LogLine)) error {
	query := url.Values{}
	if opts.Follow {
		query.Set("follow", "1")
	}
	if opts.CurrentBoot {
		query.Set("boot", "current")
	}
	if opts.Process != "" {
		query.Set("process", opts.Process)
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

// Exec runs a command in the guest, in its first process's root.
func (c *Client) Exec(ctx context.Context, id string, command []string) (ExecResponse, error) {
	return c.ExecIn(ctx, id, "", command)
}

// ExecIn runs a command in the root of the process named: InitProcess or
// empty for the first process, else a member.
func (c *Client) ExecIn(ctx context.Context, id, process string, command []string) (ExecResponse, error) {
	var out ExecResponse
	err := c.doJSON(ctx, http.MethodPost, "/v1/guests/"+id+"/exec", ExecRequest{Command: command, Process: process}, &out)
	return out, err
}

// Stop ends the guest's first process within its grace; the machine powers
// off once its members have ended too.
func (c *Client) Stop(ctx context.Context, id string) error {
	return c.doJSON(ctx, http.MethodPost, "/v1/guests/"+id+"/stop", nil, nil)
}

// LeaveNetwork has the agent dissolve the bridge it built for the network
// it holds address on, ahead of its container leaving that network.
func (c *Client) LeaveNetwork(ctx context.Context, address netip.Addr) error {
	return c.doJSON(ctx, http.MethodDelete, "/v1/networks/"+address.String(), nil, nil)
}

// SetIngress replaces the rules at the guest's tap.
func (c *Client) SetIngress(ctx context.Context, id string, stages [][]PacketRule) error {
	if stages == nil {
		stages = [][]PacketRule{}
	}
	return c.doJSON(ctx, http.MethodPut, "/v1/guests/"+id+"/ingress", stages, nil)
}

// SetRoute replaces where the guest's default route goes, in the terms of
// GuestNetwork.DefaultRoute.
func (c *Client) SetRoute(ctx context.Context, id, defaultRoute string) error {
	return c.doJSON(ctx, http.MethodPut, "/v1/guests/"+id+"/route", map[string]string{"defaultRoute": defaultRoute}, nil)
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
