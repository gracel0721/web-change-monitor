// Package fetcher retrieves raw HTML for a URL with sane defaults.
package fetcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

const userAgent = "webwatch/0.1 (+https://github.com/gvleverett/web-change-monitor)"

// Get fetches the page body as a string. It follows up to 10 redirects,
// enforces a 30s timeout, and rejects non-2xx responses.
func Get(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	client := &http.Client{
		Timeout: 30 * time.Second,
		// http.Client follows redirects by default up to 10.
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("unexpected status %d for %s", resp.StatusCode, url)
	}

	// Cap body size to avoid pulling in huge pages.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 5*1024*1024))
	if err != nil {
		return "", fmt.Errorf("read body: %w", err)
	}
	return string(body), nil
}