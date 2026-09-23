package factory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"
)

type Client struct{ http *http.Client }

func NewClient(socket string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", socket)
	}}
	return &Client{http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}
func (c *Client) do(ctx context.Context, method, path string, in, out any) error {
	var body *bytes.Reader
	if in == nil {
		body = bytes.NewReader(nil)
	} else {
		data, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://factory"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		var p struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&p)
		if p.Error == "" {
			p.Error = resp.Status
		}
		return fmt.Errorf("%s", p.Error)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
func (c *Client) Health(ctx context.Context) error {
	_, err := c.Version(ctx)
	return err
}
func (c *Client) Version(ctx context.Context) (int, error) {
	var out struct {
		APIVersion int `json:"apiVersion"`
	}
	err := c.do(ctx, "GET", "/v1/health", nil, &out)
	return out.APIVersion, err
}
func (c *Client) List(ctx context.Context) (Snapshot, error) {
	var out Snapshot
	err := c.do(ctx, "GET", "/v1/work-orders", nil, &out)
	return out, err
}
func (c *Client) Get(ctx context.Context, id string) (Snapshot, error) {
	var out Snapshot
	err := c.do(ctx, "GET", "/v1/work-orders/"+id, nil, &out)
	return out, err
}
func (c *Client) Create(ctx context.Context, in CreateRequest) (WorkOrder, error) {
	var out WorkOrder
	err := c.do(ctx, "POST", "/v1/work-orders", in, &out)
	return out, err
}
func (c *Client) Action(ctx context.Context, id, action, note string) (WorkOrder, error) {
	var out WorkOrder
	err := c.do(ctx, "POST", "/v1/work-orders/"+id+"/actions", ActionRequest{Action: action, Note: note}, &out)
	return out, err
}
func (c *Client) Delete(ctx context.Context, id string) error {
	var out map[string]bool
	return c.do(ctx, "DELETE", "/v1/work-orders/"+id, nil, &out)
}
func (c *Client) Shutdown(ctx context.Context) error {
	var out map[string]bool
	return c.do(ctx, "POST", "/v1/shutdown", map[string]any{}, &out)
}
