package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/matipan/galpon/internal/model"
)

type Client struct{ http *http.Client }

func NewClient(socket string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 30 * time.Minute}}
}

func (c *Client) Health(ctx context.Context) error {
	var out map[string]any
	return c.get(ctx, "/v1/health", &out)
}
func (c *Client) Dashboard(ctx context.Context) (model.Dashboard, error) {
	var out model.Dashboard
	err := c.get(ctx, "/v1/dashboard", &out)
	return out, err
}
func (c *Client) Agent(ctx context.Context, id string) (model.AgentView, error) {
	var out model.AgentView
	err := c.get(ctx, "/v1/agents/"+id, &out)
	return out, err
}
func (c *Client) AddRepository(ctx context.Context, request AddRepositoryRequest) (model.Repository, error) {
	var out struct {
		Repository model.Repository `json:"repository"`
	}
	err := c.post(ctx, "/v1/repositories", request, &out)
	return out.Repository, err
}
func (c *Client) AddRepositoryRemote(ctx context.Context, repository, name, fetchURL, pushURL string, pushDefault bool) (model.Repository, error) {
	var out model.Repository
	err := c.post(ctx, "/v1/repositories/"+repository+"/remotes", map[string]any{"name": name, "fetchUrl": fetchURL, "pushUrl": pushURL, "pushDefault": pushDefault}, &out)
	return out, err
}
func (c *Client) DeleteResource(ctx context.Context, kind, id string) (model.DeletionResult, error) {
	paths := map[string]string{
		"repository": "/v1/repositories/", "workspace": "/v1/workspaces/",
		"worktree": "/v1/worktrees/", "agent": "/v1/agents/",
	}
	prefix, ok := paths[kind]
	if !ok {
		return model.DeletionResult{}, fmt.Errorf("invalid resource kind %q", kind)
	}
	var out model.DeletionResult
	err := c.do(ctx, http.MethodDelete, prefix+id, nil, &out)
	return out, err
}
func (c *Client) Cleanup(ctx context.Context) (model.CleanupResult, error) {
	var out model.CleanupResult
	err := c.post(ctx, "/v1/cleanup", map[string]any{}, &out)
	return out, err
}
func (c *Client) CreateWorkspace(ctx context.Context, in CreateWorkspaceRequest) (model.Workspace, error) {
	var out model.Workspace
	err := c.post(ctx, "/v1/workspaces", in, &out)
	return out, err
}
func (c *Client) CreateWorktree(ctx context.Context, in CreateWorktreeRequest) (CreateWorktreeResult, error) {
	var out CreateWorktreeResult
	err := c.post(ctx, "/v1/worktrees", in, &out)
	return out, err
}
func (c *Client) CreateAgent(ctx context.Context, in CreateAgentRequest) (model.Agent, error) {
	var out model.Agent
	err := c.post(ctx, "/v1/agents", in, &out)
	return out, err
}
func (c *Client) OpenAgent(ctx context.Context, id string, focus bool) (model.Agent, error) {
	var out model.Agent
	err := c.post(ctx, "/v1/agents/"+id+"/open", map[string]any{"focus": focus}, &out)
	return out, err
}
func (c *Client) Send(ctx context.Context, id, text string) (model.AgentMessage, error) {
	var out model.AgentMessage
	err := c.post(ctx, "/v1/agents/"+id+"/messages", map[string]any{"text": text}, &out)
	return out, err
}
func (c *Client) StopRuntime(ctx context.Context, id, runtimeID, failure string) error {
	var out map[string]any
	return c.post(ctx, "/v1/runtime/agents/"+id+"/stop", map[string]any{"runtimeId": runtimeID, "error": failure}, &out)
}
func (c *Client) SetRenderer(ctx context.Context, workspaceID, renderer, rendererContext, id string) error {
	var out map[string]any
	return c.post(ctx, "/v1/workspaces/"+workspaceID+"/renderer", map[string]any{"renderer": renderer, "context": rendererContext, "id": id}, &out)
}
func (c *Client) Shutdown(ctx context.Context) error {
	var out map[string]any
	return c.post(ctx, "/v1/shutdown", map[string]any{}, &out)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodGet, path, nil, out)
}
func (c *Client) post(ctx context.Context, path string, in, out any) error {
	return c.do(ctx, http.MethodPost, path, in, out)
}
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://galpon"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&failure)
		if failure.Error == "" {
			failure.Error = resp.Status
		}
		return fmt.Errorf("%s", failure.Error)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
