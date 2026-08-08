// Package llmclient calls the Python summarizer subprocess, which in turn
// talks to a local Ollama model. This is the polyglot boundary: Go passes a
// JSON contract on stdin and reads JSON back on stdout.
package llmclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"
)

// Request is sent to the Python summarizer on stdin.
type Request struct {
	Before      string `json:"before"`
	After       string `json:"after"`
	Diff        string `json:"diff"`
	OllamaURL   string `json:"ollama_url"`
	OllamaModel string `json:"ollama_model"`
}

// Response is read from the Python summarizer on stdout.
type Response struct {
	Summary    string `json:"summary"`
	Importance string `json:"importance"`
	Error      string `json:"error,omitempty"`
}

// Summarize spawns python3 on summarizeScript with the given request and
// returns the model's summary + importance. A non-nil error means the LLM
// step failed; callers should treat this as non-fatal.
func Summarize(summarizeScript string, req Request, timeout time.Duration) (Response, error) {
	if timeout <= 0 {
		timeout = 3 * time.Minute
	}
	input, err := json.Marshal(req)
	if err != nil {
		return Response{}, fmt.Errorf("marshal request: %w", err)
	}

	abs, err := filepath.Abs(summarizeScript)
	if err != nil {
		return Response{}, fmt.Errorf("resolve script path: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", abs)
	cmd.Stdin = bytes.NewReader(input)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf

	if err := cmd.Run(); err != nil {
		return Response{}, fmt.Errorf("python summarize: %w (stderr: %s)", err, errBuf.String())
	}

	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &resp); err != nil {
		return Response{}, fmt.Errorf("decode summary: %w (stdout: %s)", err, out.String())
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	if resp.Summary == "" {
		return resp, errors.New("empty summary")
	}
	return resp, nil
}