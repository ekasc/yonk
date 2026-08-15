package firecracker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// ApiClient drives a Firecracker process over its Unix socket API.
type ApiClient struct {
	http *http.Client
}

// NewApiClient connects to the Firecracker API socket.
func NewApiClient(socketPath string) *ApiClient {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &ApiClient{http: &http.Client{Transport: transport, Timeout: 10 * time.Second}}
}

// Put sends one configuration request and expects HTTP 204.
func (c *ApiClient) Put(ctx context.Context, path string, body any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode %s body: %w", path, err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, "http://localhost"+path, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("create %s request: %w", path, err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("PUT %s: %w", path, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		message, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
		return fmt.Errorf("PUT %s: HTTP %d: %s", path, response.StatusCode, message)
	}
	return nil
}

// Start boots the configured microVM.
func (c *ApiClient) Start(ctx context.Context) error {
	return c.Put(ctx, "/actions", map[string]string{"action_type": "InstanceStart"})
}

// WaitForSocket blocks until the Firecracker API socket accepts connections.
func WaitForSocket(ctx context.Context, socketPath string) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		conn, err := net.Dial("unix", socketPath)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("wait for firecracker api socket: %w", err)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("firecracker api socket %q never became ready: %w", socketPath, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
