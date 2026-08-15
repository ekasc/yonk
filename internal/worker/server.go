// Package worker serves the Yonk job protocol.
package worker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ekassinghchhabra/yonk/internal/eventstream"
	"github.com/ekassinghchhabra/yonk/internal/executor"
	"github.com/ekassinghchhabra/yonk/internal/job"
	"github.com/ekassinghchhabra/yonk/internal/workspace"
)

const (
	maxJobRequestBytes      = 64 << 10
	maxWorkspaceUploadBytes = 1 << 30
	maxMultipartOverhead    = 1 << 20
	maxWorkspaceFiles       = 100_000
	maxWorkspaceBytes       = 8 << 30
)

// Server exposes worker information and streams job execution events.
type Server struct {
	info     job.WorkerInfo
	executor executor.Executor
	logger   *slog.Logger
	slots    chan struct{}
}

// NewServer creates a worker protocol handler.
func NewServer(info job.WorkerInfo, exec executor.Executor, logger *slog.Logger, maxConcurrent int) (*Server, error) {
	if info.Name == "" {
		return nil, errors.New("worker name is required")
	}
	if exec == nil {
		return nil, errors.New("executor is required")
	}
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if maxConcurrent < 1 {
		return nil, errors.New("max concurrent jobs must be positive")
	}
	return &Server{info: info, executor: exec, logger: logger, slots: make(chan struct{}, maxConcurrent)}, nil
}

// Handler returns the worker HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/worker", s.handleWorkerInfo)
	mux.HandleFunc("/v1/jobs:run", s.handleRun)
	mux.HandleFunc("/v1/jobs/", s.handleJobAction)
	return securityHeaders(mux)
}

func (s *Server) handleWorkerInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, s.info)
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "content type must be multipart/form-data")
		return
	}

	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		writeError(w, http.StatusServiceUnavailable, "worker_busy", "worker has reached its concurrent job limit")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxWorkspaceUploadBytes+maxJobRequestBytes+maxMultipartOverhead)
	multipartReader, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request is not valid multipart data")
		return
	}
	jobPart, err := multipartReader.NextPart()
	if err != nil || jobPart.FormName() != "job" {
		writeError(w, http.StatusBadRequest, "invalid_request", "first multipart field must be job")
		return
	}
	request, err := decodeJobPart(jobPart)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if err := request.Job.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_job", err.Error())
		return
	}
	if !s.supports(request.Job.Platform) {
		writeError(w, http.StatusUnprocessableEntity, "unsupported_platform", fmt.Sprintf("worker cannot execute %s", request.Job.Platform.String()))
		return
	}

	workspacePart, err := multipartReader.NextPart()
	if err != nil || workspacePart.FormName() != "workspace" {
		writeError(w, http.StatusBadRequest, "workspace_upload_failure", "second multipart field must be workspace")
		return
	}
	jobDirectory, err := os.MkdirTemp("", "yonk-job-"+request.Job.ID+"-*")
	if err != nil {
		s.logger.Error("create job directory", "job_id", request.Job.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "workspace_setup_failure", "worker could not create job state")
		return
	}
	cleaned := false
	cleanup := func() error {
		if cleaned {
			return nil
		}
		cleaned = true
		if err := os.RemoveAll(jobDirectory); err != nil {
			return fmt.Errorf("remove job directory: %w", err)
		}
		return nil
	}
	defer func() {
		if err := cleanup(); err != nil {
			s.logger.Error("remove job directory", "job_id", request.Job.ID, "path", jobDirectory, "error", err)
		}
	}()
	workspaceRoot := filepath.Join(jobDirectory, "workspace")
	if err := os.Mkdir(workspaceRoot, 0o700); err != nil {
		writeError(w, http.StatusInternalServerError, "workspace_setup_failure", "worker could not create workspace")
		return
	}
	extractionLimit := int64(maxWorkspaceBytes)
	if request.Job.Resources.DiskMB < int(maxWorkspaceBytes>>20) {
		extractionLimit = int64(request.Job.Resources.DiskMB) << 20
	}
	workspaceStats, err := workspace.Extract(r.Context(), io.LimitReader(workspacePart, maxWorkspaceUploadBytes+1), workspaceRoot, workspace.Limits{
		MaxFiles:             maxWorkspaceFiles,
		MaxUncompressedBytes: extractionLimit,
	})
	if err != nil {
		s.logger.Warn("workspace rejected", "job_id", request.Job.ID, "error", err)
		writeError(w, http.StatusBadRequest, "workspace_upload_failure", err.Error())
		return
	}
	if workspaceStats.CompressedBytes > maxWorkspaceUploadBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "workspace_upload_failure", "compressed workspace exceeds worker upload limit")
		return
	}
	if extraPart, err := multipartReader.NextPart(); err != io.EOF || extraPart != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "request contains unexpected multipart fields")
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming_unavailable", "server does not support response streaming")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	sink := eventstream.NewSink(w, flusher.Flush)
	if err := sink.Emit(job.Event{Type: job.EventStatus, Status: "workspace_ready"}); err != nil {
		return
	}
	if err := sink.Emit(job.Event{Type: job.EventStatus, Status: "running"}); err != nil {
		return
	}

	s.logger.Info("job started",
		"job_id", request.Job.ID,
		"command", request.Job.Command,
		"platform", request.Job.Platform.String(),
		"workspace_files", workspaceStats.Files,
		"workspace_bytes", workspaceStats.UncompressedBytes,
	)
	work := workspace.Workspace{Root: workspaceRoot, Stats: workspaceStats}
	result, executionErr := s.executor.Run(r.Context(), request.Job, work, eventstream.Writer{Sink: sink, EventType: job.EventStdout}, eventstream.Writer{Sink: sink, EventType: job.EventStderr})
	result.Worker = s.info.Name
	result.BytesUploaded = workspaceStats.CompressedBytes
	result.BytesDownloaded = sink.DataBytes()
	cleanupErr := cleanup()
	if cleanupErr != nil {
		s.logger.Error("job cleanup failed", "job_id", request.Job.ID, "path", jobDirectory, "error", cleanupErr)
		_ = sink.Emit(job.Event{Type: job.EventFailure, Failure: &job.Failure{Code: "cleanup_failure", Message: cleanupErr.Error()}})
		return
	}
	if err := sink.Emit(job.Event{Type: job.EventStatus, Status: "cleaned"}); err != nil {
		return
	}
	if executionErr != nil {
		s.logger.Error("job execution failed", "job_id", request.Job.ID, "error", executionErr)
		_ = sink.Emit(job.Event{Type: job.EventFailure, Failure: &job.Failure{Code: "executor_failure", Message: executionErr.Error()}})
		return
	}

	s.logger.Info("job completed",
		"job_id", request.Job.ID,
		"exit_code", result.ExitCode,
		"duration_ms", result.DurationMillis,
		"termination_reason", result.TerminationReason,
	)
	_ = sink.Emit(job.Event{Type: job.EventCompletion, Result: &result})
}

func decodeJobPart(part io.Reader) (job.RunRequest, error) {
	decoder := json.NewDecoder(io.LimitReader(part, maxJobRequestBytes+1))
	decoder.DisallowUnknownFields()
	var request job.RunRequest
	if err := decoder.Decode(&request); err != nil {
		return job.RunRequest{}, errors.New("job field is not valid Yonk JSON")
	}
	if err := ensureEOF(decoder); err != nil {
		return job.RunRequest{}, errors.New("job field must contain one JSON value within the size limit")
	}
	return request, nil
}

func (s *Server) handleJobAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, ":cancel") {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	jobID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v1/jobs/"), ":cancel")
	if jobID == "" || strings.Contains(jobID, "/") {
		writeError(w, http.StatusBadRequest, "invalid_job_id", "job id is invalid")
		return
	}
	if err := s.executor.Kill(r.Context(), jobID); err != nil {
		writeError(w, http.StatusNotFound, "job_not_running", "job is not running")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) supports(platform job.Platform) bool {
	for _, capability := range s.info.Executors {
		if capability.Platform == platform {
			return true
		}
	}
	return false
}

func ensureEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("extra JSON value")
		}
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("write JSON response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, job.ErrorResponse{Code: code, Message: message})
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// HTTPServer applies conservative timeouts suitable for the protocol. A zero
// WriteTimeout is intentional because job streams can outlive a fixed write deadline.
func HTTPServer(address string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Minute,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}
