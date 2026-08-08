// Package notify renders the human-facing "🚨 CHANGE DETECTED" block and
// optionally forwards change events to a webhook.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ChangeView is the data needed to render a notification.
type ChangeView struct {
	URL        string
	Name       string
	OldContent string
	NewContent string
	Diff       string
	Summary    string
	Importance string
	OccurredAt time.Time
}

// PrintChange writes the spec's change block to w.
func PrintChange(w io.Writer, c ChangeView) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "🚨 CHANGE DETECTED")
	fmt.Fprintln(w)
	fmt.Fprintln(w, c.URL)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Before:")
	fmt.Fprintln(w, indentBlock(c.OldContent, 2))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "After:")
	fmt.Fprintln(w, indentBlock(c.NewContent, 2))
	fmt.Fprintln(w)
	if c.Summary != "" {
		fmt.Fprintln(w, "AI SUMMARY:")
		fmt.Fprintln(w, c.Summary)
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "AI SUMMARY: (unavailable — LLM failed or not configured)")
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, "Importance: "+strings.ToUpper(c.Importance))
	fmt.Fprintln(w)
}

// indentBlock prepends spaces to each line of a (possibly multi-line) block.
func indentBlock(s string, n int) string {
	pad := strings.Repeat(" ", n)
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// PostWebhook sends the change to a webhook URL as JSON if one is configured.
// It is best-effort: errors are returned but callers should not abort.
func PostWebhook(webhookURL string, c ChangeView) error {
	if webhookURL == "" {
		return nil
	}
	payload := map[string]any{
		"url":        c.URL,
		"name":       c.Name,
		"diff":       c.Diff,
		"summary":    c.Summary,
		"importance": strings.ToUpper(c.Importance),
		"occurred_at": c.OccurredAt,
	}
	body, _ := json.Marshal(payload)
	resp, err := http.Post(webhookURL, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}