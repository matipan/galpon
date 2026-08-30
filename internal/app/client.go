package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/matipan/galpon/internal/model"
	"github.com/matipan/galpon/internal/store"
)

type Client struct{ http *http.Client }

type APIError struct {
	StatusCode int
	Message    string
}

func (e *APIError) Error() string { return e.Message }

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
func (c *Client) CompanionDashboard(ctx context.Context) (model.Dashboard, error) {
	var out model.Dashboard
	err := c.get(ctx, "/v1/companion/dashboard", &out)
	return out, err
}
func (c *Client) AgentOperations(ctx context.Context, id string) (model.AgentOperations, error) {
	var out model.AgentOperations
	err := c.get(ctx, "/v1/agents/"+url.PathEscape(id)+"/operations", &out)
	return out, err
}
func (c *Client) Agent(ctx context.Context, id string) (model.AgentView, error) {
	var out model.AgentView
	err := c.get(ctx, "/v1/agents/"+id, &out)
	return out, err
}
func (c *Client) AgentWork(ctx context.Context, id string, includeSettled bool) (model.WorkProjection, error) {
	var out model.WorkProjection
	path := "/v1/agents/" + url.PathEscape(id) + "/work"
	if includeSettled {
		path += "?all=1"
	}
	err := c.get(ctx, path, &out)
	return out, err
}
func (c *Client) CompanionAgent(ctx context.Context, id string, representedMessageIDs []string, messageBefore string, includeMessagePage bool) (CompanionAgentState, error) {
	query := url.Values{}
	for _, messageID := range representedMessageIDs {
		query.Add("message", messageID)
	}
	if messageBefore != "" {
		query.Set("messageBefore", messageBefore)
	}
	if includeMessagePage {
		query.Set("messagePage", "1")
	}
	path := "/v1/companion/agents/" + id
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	var out CompanionAgentState
	err := c.get(ctx, path, &out)
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
func (c *Client) CreateCheckpoint(ctx context.Context, path, passphrase string, allowLocalRemotes bool) (CheckpointResult, error) {
	var out CheckpointResult
	err := c.post(ctx, "/v1/checkpoints", map[string]any{"path": path, "passphrase": passphrase, "allowLocalRemotes": allowLocalRemotes}, &out)
	return out, err
}
func (c *Client) RestoreCheckpoint(ctx context.Context, path, passphrase string) (RestoreCheckpointResult, error) {
	var out RestoreCheckpointResult
	err := c.post(ctx, "/v1/checkpoints/restore", map[string]any{"path": path, "passphrase": passphrase}, &out)
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
func (c *Client) SendCompanion(ctx context.Context, id, prompt, idempotencyKey string) (model.AgentMessage, error) {
	return c.SendCompanionImages(ctx, id, prompt, idempotencyKey, nil)
}
func (c *Client) SendCompanionImages(ctx context.Context, id, prompt, idempotencyKey string, images []model.ImageAttachment) (model.AgentMessage, error) {
	var out model.AgentMessage
	err := c.doWithHeaders(ctx, http.MethodPost, "/v1/companion/agents/"+id+"/messages", map[string]any{"prompt": prompt, "images": images}, &out, map[string]string{"Idempotency-Key": idempotencyKey})
	return out, err
}
func (c *Client) CreateAgentFromSource(ctx context.Context, in CreateAgentFromSourceRequest, idempotencyKey string) (CreateAgentFromSourceResult, error) {
	var out CreateAgentFromSourceResult
	err := c.doWithHeaders(ctx, http.MethodPost, "/v1/companion/agents", in, &out, map[string]string{"Idempotency-Key": idempotencyKey})
	return out, err
}
func (c *Client) CommunicationProtocol(ctx context.Context) (CommunicationProtocolState, error) {
	var out CommunicationProtocolState
	err := c.get(ctx, "/v1/communication/protocol", &out)
	return out, err
}
func (c *Client) UpgradeCommunicationV2(ctx context.Context, request map[string]any) (CommunicationUpgradeResult, error) {
	var out CommunicationUpgradeResult
	err := c.post(ctx, "/v1/communication/upgrade", request, &out)
	return out, err
}
func (c *Client) RecoverCommunicationRuntime(ctx context.Context, agentID, runtimeID string) (store.CommunicationRuntimeRecoveryResult, error) {
	var out store.CommunicationRuntimeRecoveryResult
	err := c.post(ctx, "/v1/communication/recover-runtime", map[string]any{"agentId": agentID, "runtimeId": runtimeID}, &out)
	return out, err
}
func (c *Client) PrepareRuntime(ctx context.Context, id, runtimeID string) error {
	var out map[string]any
	return c.post(ctx, "/v1/runtime/agents/"+id+"/prepare", map[string]any{"runtimeId": runtimeID}, &out)
}
func (c *Client) RegisterRuntime(ctx context.Context, id, runtimeID, sessionID, sessionPath string, generation int) (CommunicationProtocolState, error) {
	var out struct {
		Protocol CommunicationProtocolState `json:"protocol"`
	}
	err := c.post(ctx, "/v1/runtime/agents/"+id+"/register", map[string]any{"runtimeId": runtimeID, "sessionId": sessionID, "sessionPath": sessionPath, "protocolGeneration": generation}, &out)
	return out.Protocol, err
}
func (c *Client) RegisterDirectOperation(ctx context.Context, id string, request DirectOperationRequest) (model.AgentOperation, error) {
	var out model.AgentOperation
	err := c.post(ctx, "/v1/runtime/agents/"+id+"/operations/direct", request, &out)
	return out, err
}
func (c *Client) ClaimCoordinationOperation(ctx context.Context, id, runtimeID, claimID string, generation int) (*CoordinationOperationDelivery, error) {
	var out struct {
		Delivery *CoordinationOperationDelivery `json:"delivery"`
	}
	err := c.post(ctx, "/v1/runtime/agents/"+id+"/operations/claim", map[string]any{"runtimeId": runtimeID, "claimId": claimID, "protocolGeneration": generation}, &out)
	return out.Delivery, err
}
func (c *Client) StartCoordinationOperation(ctx context.Context, agentID, operationID, runtimeID string, attempt int) error {
	var out map[string]any
	return c.post(ctx, "/v1/runtime/agents/"+agentID+"/operations/"+url.PathEscape(operationID)+"/start", map[string]any{"runtimeId": runtimeID, "attempt": attempt}, &out)
}
func (c *Client) RenewCoordinationOperation(ctx context.Context, agentID, operationID, runtimeID string, attempt int) error {
	var out map[string]any
	return c.post(ctx, "/v1/runtime/agents/"+agentID+"/operations/"+url.PathEscape(operationID)+"/renew", map[string]any{"runtimeId": runtimeID, "attempt": attempt}, &out)
}
func (c *Client) SettleCoordinationOperation(ctx context.Context, agentID, operationID, runtimeID string, attempt int, response, failure string) (store.AgentOperationSettleResult, error) {
	var out store.AgentOperationSettleResult
	err := c.post(ctx, "/v1/runtime/agents/"+agentID+"/operations/"+url.PathEscape(operationID)+"/settle", map[string]any{"runtimeId": runtimeID, "attempt": attempt, "response": response, "error": failure}, &out)
	return out, err
}
func (c *Client) TakeCoordinationReceipts(ctx context.Context, agentID, operationID, runtimeID string, attempt int, toolRequestID string) (CoordinationReceiptBatch, error) {
	var out CoordinationReceiptBatch
	err := c.post(ctx, "/v1/runtime/agents/"+agentID+"/operations/"+url.PathEscape(operationID)+"/receipts/take", map[string]any{"runtimeId": runtimeID, "attempt": attempt, "toolRequestId": toolRequestID}, &out)
	return out, err
}
func (c *Client) PresentCoordinationReceipt(ctx context.Context, agentID, operationID, receiptID, runtimeID string, attempt int, toolRequestID string) error {
	var out map[string]any
	return c.post(ctx, "/v1/runtime/agents/"+agentID+"/operations/"+url.PathEscape(operationID)+"/receipts/"+url.PathEscape(receiptID)+"/present", map[string]any{"runtimeId": runtimeID, "attempt": attempt, "toolRequestId": toolRequestID}, &out)
}
func (c *Client) AcknowledgeCoordinationReceipt(ctx context.Context, agentID, operationID, receiptID, runtimeID string, attempt int) error {
	var out map[string]any
	return c.post(ctx, "/v1/runtime/agents/"+agentID+"/operations/"+url.PathEscape(operationID)+"/receipts/"+url.PathEscape(receiptID)+"/ack", map[string]any{"runtimeId": runtimeID, "attempt": attempt}, &out)
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
	return c.doWithHeaders(ctx, method, path, in, out, nil)
}
func (c *Client) doWithHeaders(ctx context.Context, method, path string, in, out any, headers map[string]string) error {
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
	for name, value := range headers {
		req.Header.Set(name, value)
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
		return &APIError{StatusCode: resp.StatusCode, Message: failure.Error}
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
