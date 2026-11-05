// Package ao implements the Kernel's execution seam against a running
// Agent Orchestrator daemon. It owns no task state: it only creates sessions,
// observes turns, interrupts them, and reads workspace artifacts.
package ao

import (
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

	"projectos/kernel/internal/domain"
	"projectos/kernel/internal/exec"
)

type Config struct {
	BaseURL   string // e.g. http://127.0.0.1:49380
	ProjectID string
	Harness   string // opencode-v2
	Model     string // provider/model
	DataDir   string // AO_DATA_DIR, used to match chat-host processes
	Client    *http.Client
	// StartTimeout covers session creation, which waits for the chat driver to
	// come up; a short HTTP timeout turns a slow but healthy spawn into a failure.
	StartTimeout time.Duration
}

type Backend struct {
	cfg Config
}

func New(cfg Config) *Backend {
	if cfg.Harness == "" {
		cfg.Harness = "opencode-v2"
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.StartTimeout == 0 {
		cfg.StartTimeout = 3 * time.Minute
	}
	return &Backend{cfg: cfg}
}

func (b *Backend) Name() string { return "ao" }

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (b *Backend) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(b.cfg.BaseURL, "/")+path, reader)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.cfg.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, err
	}
	if resp.StatusCode >= 300 {
		var apiErr apiError
		_ = json.Unmarshal(raw, &apiErr)
		return resp.StatusCode, fmt.Errorf("ao %s %s: %d %s: %s", method, path, resp.StatusCode, apiErr.Code, apiErr.Message)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, err
		}
	}
	return resp.StatusCode, nil
}

// Start creates one worker session. clientRequestId makes a retried Start
// idempotent: AO replays the original session instead of spawning a second one.
func (b *Backend) Start(ctx context.Context, req exec.StartRequest) (exec.StartResult, error) {
	key := fmt.Sprintf("kernel-%s-%d", req.TaskID, req.Attempt)
	if req.Key != "" {
		key = fmt.Sprintf("kernel-%s-%s", req.TaskID, req.Key)
	}
	body := map[string]any{
		"projectId":       b.cfg.ProjectID,
		"kind":            "worker",
		"harness":         b.cfg.Harness,
		"mode":            "chat",
		"model":           b.cfg.Model,
		"prompt":          req.Prompt,
		"displayName":     fmt.Sprintf("%s-%d", req.TaskID, req.Attempt),
		"clientRequestId": key,
	}
	if req.ApprovalMode != "" {
		body["approvalMode"] = req.ApprovalMode
	}
	var out struct {
		Session struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	startCtx, cancel := context.WithTimeout(ctx, b.cfg.StartTimeout)
	defer cancel()
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		_, lastErr = b.do(startCtx, http.MethodPost, "/api/v1/sessions", body, &out)
		if lastErr == nil {
			break
		}
		if !isTransportError(lastErr) {
			break
		}
		// The idempotency key is stable, so replaying the POST returns the session
		// that may already exist instead of creating a second one.
		time.Sleep(time.Duration(attempt+1) * 500 * time.Millisecond)
	}
	if lastErr != nil {
		return exec.StartResult{}, lastErr
	}
	if out.Session.ID == "" {
		return exec.StartResult{}, fmt.Errorf("ao: spawn returned no session id")
	}
	return exec.StartResult{ExternalID: out.Session.ID}, nil
}

type conversation struct {
	Controller string `json:"controller"`
	Turns      []struct {
		ID    string `json:"id"`
		State string `json:"state"`
	} `json:"turns"`
	Messages []struct {
		Role string `json:"role"`
		Text string `json:"text"`
	} `json:"messages"`
}

func (b *Backend) conversation(ctx context.Context, sessionID string) (conversation, error) {
	var conv conversation
	_, err := b.do(ctx, http.MethodGet, "/api/v1/sessions/"+sessionID+"/conversation", nil, &conv)
	return conv, err
}

func (b *Backend) Observe(ctx context.Context, externalID string) (exec.State, error) {
	conv, err := b.conversation(ctx, externalID)
	if err != nil {
		// Transport failures are not evidence about the run; the Engine decides
		// how many consecutive failures it tolerates before failing closed.
		return exec.State{}, err
	}
	if conv.Controller == "busy" {
		return exec.State{Status: domain.RunRunning}, nil
	}
	if len(conv.Turns) == 0 {
		return exec.State{Status: domain.RunRunning}, nil
	}
	switch conv.Turns[len(conv.Turns)-1].State {
	case "completed":
		return exec.State{Status: domain.RunSucceeded, Message: "turn completed"}, nil
	case "interrupted", "cancelled":
		return exec.State{Status: domain.RunCancelled, Message: "turn interrupted"}, nil
	case "failed", "error":
		return exec.State{Status: domain.RunFailed, Message: "turn failed"}, nil
	default:
		return exec.State{Status: domain.RunRunning}, nil
	}
}

// Cancel interrupts the in-flight turn and then reaps AO's detached chat host.
// The host survives daemon shutdown by design, so interrupting alone leaks it.
func (b *Backend) Cancel(ctx context.Context, externalID string) error {
	code, err := b.do(ctx, http.MethodPost, "/api/v1/sessions/"+externalID+"/conversation/interrupt", nil, nil)
	reapErr := error(nil)
	if _, rerr := Reap(externalID, b.cfg.DataDir); rerr != nil {
		reapErr = rerr
	}
	if err != nil && code != http.StatusConflict {
		return err
	}
	return reapErr
}

type workspaceFiles struct {
	WorkspaceVersion string `json:"workspaceVersion"`
	Files            []struct {
		Path   string `json:"path"`
		Status string `json:"status"`
	} `json:"files"`
}

type workspaceFile struct {
	Content         string `json:"content"`
	FileFingerprint string `json:"fileFingerprint"`
}

// Collect returns the verified artifact. When artifactPath is set the content
// is read from the session worktree, never from the agent's own claim.
func (b *Backend) Collect(ctx context.Context, externalID, artifactPath string) (exec.Artifact, error) {
	var files workspaceFiles
	_, err := b.do(ctx, http.MethodGet, "/api/v1/sessions/"+externalID+"/workspace/files", nil, &files)
	if err != nil {
		return exec.Artifact{}, err
	}
	artifact := exec.Artifact{Version: files.WorkspaceVersion}
	// Usage comes from the conversation; if it cannot be read the artifact is not
	// verifiable under budget, so fail closed instead of counting zero tokens.
	conv, cerr := b.conversation(ctx, externalID)
	if cerr != nil {
		return artifact, fmt.Errorf("collect usage: %w", cerr)
	}
	for _, m := range conv.Messages {
		switch m.Role {
		case "user":
			artifact.PromptChars += len(m.Text)
			artifact.PromptTokens = artifact.PromptChars / 4
			artifact.UsageEstimated = true
		case "assistant":
			artifact.OutputChars += len(m.Text)
			artifact.OutputTokens = artifact.OutputChars / 4
		}
	}
	for _, f := range files.Files {
		if f.Status != "unmodified" && f.Path != "" {
			artifact.Files = append(artifact.Files, f.Path)
		}
	}
	if artifactPath == "" {
		for i := len(conv.Messages) - 1; i >= 0; i-- {
			if conv.Messages[i].Role == "assistant" {
				artifact.Result = conv.Messages[i].Text
				break
			}
		}
		return artifact, nil
	}
	var file workspaceFile
	_, err = b.do(ctx, http.MethodGet,
		"/api/v1/sessions/"+externalID+"/workspace/file?path="+urlQueryEscape(artifactPath), nil, &file)
	if err != nil {
		return artifact, err
	}
	artifact.Result = file.Content
	if file.FileFingerprint != "" {
		artifact.Version = file.FileFingerprint
	}
	return artifact, nil
}

// isTransportError reports whether the call never produced an HTTP response, so
// retrying with the same idempotency key is safe.
func isTransportError(err error) bool {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF)
}

func urlQueryEscape(s string) string {
	r := strings.NewReplacer("%", "%25", " ", "%20", "#", "%23", "&", "%26", "?", "%3F", "+", "%2B")
	return r.Replace(s)
}
