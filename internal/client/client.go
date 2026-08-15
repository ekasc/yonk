// Package client implements the Yonk worker protocol client.
package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/ekasc/yonk/internal/job"
)

const defaultPort = "9665"

// Client communicates with one worker endpoint over HTTP. It has no knowledge
// of how packets reach that endpoint.
type Client struct {
	baseURL   *url.URL
	http      *http.Client
	userAgent string
}

// RemoteError is a structured worker rejection or execution failure.
type RemoteError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *RemoteError) Error() string {
	if e.StatusCode != 0 {
		return fmt.Sprintf("worker rejected request (%s, HTTP %d): %s", e.Code, e.StatusCode, e.Message)
	}
	return fmt.Sprintf("remote execution failed (%s): %s", e.Code, e.Message)
}

// New creates a protocol client for endpoint. Bare hostnames and host:port
// values are accepted; HTTP and port 9665 are defaults for milestone 1.
func New(endpoint string, httpClient *http.Client) (*Client, error) {
	normalized, err := NormalizeEndpoint(endpoint)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, fmt.Errorf("parse worker endpoint: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{}
	}
	return &Client{baseURL: parsed, http: httpClient, userAgent: "yonk/0.1"}, nil
}

// NormalizeEndpoint converts a worker endpoint into an HTTP base URL.
func NormalizeEndpoint(endpoint string) (string, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return "", errors.New("worker endpoint is required")
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "http://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", fmt.Errorf("parse worker endpoint: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported worker endpoint scheme %q", u.Scheme)
	}
	if u.Hostname() == "" || u.User != nil {
		return "", errors.New("worker endpoint must contain a hostname and no user information")
	}
	if u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("worker endpoint must not contain a path, query, or fragment")
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), defaultPort)
	}
	u.Path = ""
	return strings.TrimSuffix(u.String(), "/"), nil
}

// NewJobID generates a non-secret, collision-resistant job identifier.
func NewJobID() (string, error) {
	var random [12]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", fmt.Errorf("generate job id: %w", err)
	}
	return "job_" + hex.EncodeToString(random[:]), nil
}

// WorkerInfo retrieves worker capabilities.
func (c *Client) WorkerInfo(ctx context.Context) (job.WorkerInfo, error) {
	request, err := c.request(ctx, http.MethodGet, "/v1/worker", nil)
	if err != nil {
		return job.WorkerInfo{}, err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return job.WorkerInfo{}, fmt.Errorf("connect to worker: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return job.WorkerInfo{}, decodeRemoteError(response)
	}
	var info job.WorkerInfo
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&info); err != nil {
		return job.WorkerInfo{}, fmt.Errorf("decode worker information: %w", err)
	}
	if info.Name == "" || len(info.Executors) == 0 {
		return job.WorkerInfo{}, errors.New("worker returned incomplete capability information")
	}
	return info, nil
}

// Run uploads a workspace, starts a job, and calls handle for each streamed
// event. It returns only after a completion event or a protocol/execution failure.
func (c *Client) Run(ctx context.Context, spec job.Job, archive io.Reader, handle func(job.Event) error) (job.Result, error) {
	if archive == nil {
		return job.Result{}, errors.New("workspace archive is required")
	}
	body, contentType, uploadResult, err := multipartRunBody(spec, archive)
	if err != nil {
		return job.Result{}, err
	}
	request, err := c.request(ctx, http.MethodPost, "/v1/jobs:run", body)
	if err != nil {
		_ = body.Close()
		return job.Result{}, err
	}
	request.Header.Set("Content-Type", contentType)
	response, err := c.http.Do(request)
	if err != nil {
		_ = body.Close()
		uploadErr := <-uploadResult
		if uploadErr != nil && !errors.Is(uploadErr, io.ErrClosedPipe) {
			return job.Result{}, fmt.Errorf("upload workspace: %w", uploadErr)
		}
		return job.Result{}, fmt.Errorf("submit job: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_ = body.Close()
		<-uploadResult
		return job.Result{}, decodeRemoteError(response)
	}
	if uploadErr := <-uploadResult; uploadErr != nil {
		return job.Result{}, fmt.Errorf("upload workspace: %w", uploadErr)
	}

	decoder := json.NewDecoder(response.Body)
	for {
		var event job.Event
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				return job.Result{}, errors.New("worker closed the stream before completion")
			}
			return job.Result{}, fmt.Errorf("decode job event: %w", err)
		}
		if handle != nil {
			if err := handle(event); err != nil {
				return job.Result{}, fmt.Errorf("handle job event: %w", err)
			}
		}
		switch event.Type {
		case job.EventStatus, job.EventStdout, job.EventStderr, job.EventArtifact:
			continue
		case job.EventCompletion:
			if event.Result == nil {
				return job.Result{}, errors.New("completion event has no result")
			}
			return *event.Result, nil
		case job.EventFailure:
			if event.Failure == nil {
				return job.Result{}, errors.New("failure event has no details")
			}
			return job.Result{}, &RemoteError{Code: event.Failure.Code, Message: event.Failure.Message}
		default:
			return job.Result{}, fmt.Errorf("worker sent unknown event type %q", event.Type)
		}
	}
}

// Cancel asks the worker to terminate a running job.
func (c *Client) Cancel(ctx context.Context, jobID string) error {
	if jobID == "" || strings.Contains(jobID, "/") {
		return errors.New("job id is invalid")
	}
	request, err := c.request(ctx, http.MethodPost, "/v1/jobs/"+url.PathEscape(jobID)+":cancel", nil)
	if err != nil {
		return err
	}
	response, err := c.http.Do(request)
	if err != nil {
		return fmt.Errorf("cancel job: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		return decodeRemoteError(response)
	}
	return nil
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	u := *c.baseURL
	u.Path = path
	request, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create worker request: %w", err)
	}
	request.Header.Set("Accept", "application/json, application/x-ndjson")
	request.Header.Set("User-Agent", c.userAgent)
	return request, nil
}

func multipartRunBody(spec job.Job, archive io.Reader) (io.ReadCloser, string, <-chan error, error) {
	encodedJob, err := json.Marshal(job.RunRequest{Job: spec})
	if err != nil {
		return nil, "", nil, fmt.Errorf("encode job request: %w", err)
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	result := make(chan error, 1)
	go func() {
		var writeErr error
		jobPart, err := multipartWriter.CreateFormField("job")
		if err == nil {
			_, err = io.Copy(jobPart, bytes.NewReader(encodedJob))
		}
		if err == nil {
			var workspacePart io.Writer
			workspacePart, err = multipartWriter.CreateFormFile("workspace", "workspace.tar.gz")
			if err == nil {
				_, err = io.Copy(workspacePart, archive)
			}
		}
		if err == nil {
			err = multipartWriter.Close()
		}
		if err != nil {
			writeErr = fmt.Errorf("write multipart workspace request: %w", err)
			_ = writer.CloseWithError(writeErr)
		} else {
			_ = writer.Close()
		}
		result <- writeErr
		close(result)
	}()
	return reader, multipartWriter.FormDataContentType(), result, nil
}

func decodeRemoteError(response *http.Response) error {
	var remote job.ErrorResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&remote); err != nil {
		return fmt.Errorf("worker returned HTTP %d with an invalid error response", response.StatusCode)
	}
	return &RemoteError{StatusCode: response.StatusCode, Code: remote.Code, Message: remote.Message}
}
