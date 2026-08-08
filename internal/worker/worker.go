// Package worker runs the fetch -> normalize -> compare -> (summarize) ->
// persist/notify pipeline for one or more websites.
package worker

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"webwatch/internal/compare"
	"webwatch/internal/config"
	"webwatch/internal/db"
	"webwatch/internal/fetcher"
	"webwatch/internal/llmclient"
	"webwatch/internal/normalize"
	"webwatch/internal/notify"
	"webwatch/internal/render"
)

// Worker holds the dependencies shared across checks.
type Worker struct {
	Store           *db.Store
	Settings        config.Settings
	SummarizeScript string
	NotifyOut       io.Writer // where change blocks are printed (default os.Stdout)
}

// New builds a Worker with sensible defaults. summarizeScript defaults to
// ./llm/summarize.py relative to the current working directory.
func New(store *db.Store, settings config.Settings) *Worker {
	// Configure the headless renderer with the user's Chrome path (empty =
	// chromedp autodetect). Must happen before the first render.Render call.
	render.Configure(settings.ChromePath)
	return &Worker{
		Store:           store,
		Settings:        settings,
		SummarizeScript: defaultScriptPath(),
		NotifyOut:       os.Stdout,
	}
}

func defaultScriptPath() string {
	// Prefer the in-repo script if present (e.g. run from project root),
	// otherwise fall back to an absolute best guess next to the binary.
	if _, err := os.Stat("llm/summarize.py"); err == nil {
		return "llm/summarize.py"
	}
	return "llm/summarize.py"
}

// CheckResult summarizes the outcome of a single check.
type CheckResult struct {
	Website *db.Website
	Status  string // "unchanged" | "changed" | "new" | "error"
	Change  *db.Change
	Err     error
}

// Check runs the pipeline for one website by id.
func (w *Worker) Check(ctx context.Context, websiteID int64) CheckResult {
	site, err := w.Store.GetWebsite(websiteID)
	if err != nil {
		return CheckResult{Status: "error", Err: fmt.Errorf("load website: %w", err)}
	}
	return w.checkSite(ctx, site)
}

// CheckAll runs the pipeline for every due website.
func (w *Worker) CheckAll(ctx context.Context) []CheckResult {
	due, err := w.Store.DueWebsites(time.Now())
	if err != nil {
		return []CheckResult{{Status: "error", Err: fmt.Errorf("query due: %w", err)}}
	}
	var results []CheckResult
	for _, site := range due {
		results = append(results, w.checkSite(ctx, site))
	}
	return results
}

func (w *Worker) checkSite(ctx context.Context, site *db.Website) CheckResult {
	res := CheckResult{Website: site, Status: "error"}

	// JS-heavy sites are fetched via a headless browser so client-rendered
	// content is captured; everything else uses plain HTTP.
	var (
		raw string
		err error
	)
	if site.RenderJS {
		raw, err = render.Render(ctx, site.URL, 60*time.Second)
	} else {
		raw, err = fetcher.Get(ctx, site.URL)
	}
	if err != nil {
		w.mark(site, "error", err.Error())
		res.Err = err
		return res
	}

	content := normalize.Text([]byte(raw))
	hash := compare.Hash(content)

	prev, err := w.Store.LatestSnapshot(site.ID)
	if err != nil && err != sql.ErrNoRows {
		// Only real query errors are fatal here; "no rows" means first check.
		res.Err = fmt.Errorf("latest snapshot: %w", err)
		return res
	}
	isFirst := (err == sql.ErrNoRows)

	newSnap, err := w.Store.InsertSnapshot(site.ID, hash, content)
	if err != nil {
		res.Err = fmt.Errorf("insert snapshot: %w", err)
		return res
	}

	// First-ever check: seed baseline, no diff.
	if isFirst {
		w.mark(site, "new", "")
		res.Status = "new"
		return res
	}

	if hash == prev.ContentHash {
		w.mark(site, "unchanged", "")
		res.Status = "unchanged"
		return res
	}

	// Changed: diff, summarize, persist, notify.
	diff := compare.Diff(prev.Content, content)
	summary, importance := w.summarize(prev.Content, content, diff)

	oldID := prev.ID
	ch, err := w.Store.InsertChange(site.ID, &oldID, newSnap.ID, diff, summary, importance)
	if err != nil {
		res.Err = fmt.Errorf("insert change: %w", err)
		w.mark(site, "changed", "")
		return res
	}
	res.Change = ch
	res.Status = "changed"
	w.mark(site, "changed", "")

	w.notify(notify.ChangeView{
		URL:        site.URL,
		Name:       site.Name,
		OldContent: trimPreview(prev.Content),
		NewContent: trimPreview(content),
		Diff:       diff,
		Summary:    summary,
		Importance: importance,
		OccurredAt: time.Now(),
	})
	return res
}

func (w *Worker) summarize(before, after, diff string) (summary, importance string) {
	if w.Settings.OllamaURL == "" || w.Settings.OllamaModel == "" {
		return "", ""
	}
	resp, err := llmclient.Summarize(w.SummarizeScript, llmclient.Request{
		Before:      before,
		After:       after,
		Diff:        diff,
		OllamaURL:   w.Settings.OllamaURL,
		OllamaModel: w.Settings.OllamaModel,
	}, 3*time.Minute)
	if err != nil {
		log.Printf("llm summary failed for %s: %v", "site", err)
		return "", ""
	}
	return resp.Summary, resp.Importance
}

func (w *Worker) mark(site *db.Website, status, lastErr string) {
	next := time.Now().Add(time.Duration(site.CheckFrequencySeconds) * time.Second)
	if err := w.Store.MarkChecked(site.ID, status, lastErr, next); err != nil {
		log.Printf("mark checked: %v", err)
	}
}

func (w *Worker) notify(c notify.ChangeView) {
	notify.PrintChange(w.NotifyOut, c)
	if err := notify.PostWebhook(w.Settings.WebhookURL, c); err != nil {
		log.Printf("webhook: %v", err)
	}
}

// trimPreview keeps the before/after blocks readable in the terminal.
func trimPreview(s string) string {
	const max = 800
	if len(s) <= max {
		return s
	}
	return s[:max] + "\n[...truncated...]"
}

// ResolveScript makes the script path absolute against the executable's
// directory when the relative path can't be found at runtime.
func (w *Worker) ResolveScript() error {
	if filepath.IsAbs(w.SummarizeScript) {
		return nil
	}
	if _, err := os.Stat(w.SummarizeScript); err == nil {
		return nil
	}
	if exe, err := os.Executable(); err == nil {
		cand := filepath.Join(filepath.Dir(exe), "llm", "summarize.py")
		if _, err := os.Stat(cand); err == nil {
			w.SummarizeScript = cand
			return nil
		}
	}
	return fmt.Errorf("summarize.py not found at %q", w.SummarizeScript)
}