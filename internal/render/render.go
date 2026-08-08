// Package render fetches a page with a headless Chromium (via chromedp) so that
// client-side JavaScript has executed before the HTML is captured. This is the
// JS-aware counterpart to internal/fetcher for sites that render content in the
// browser.
package render

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
)

// settleAfterLoad is how long to wait after navigation for async JS (XHRs,
// setTimeout, hydration) to populate the DOM before snapshotting. Pragmatic
// MVP value; replace with wait-for-selector / network-idle later.
const settleAfterLoad = 2 * time.Second

var (
	once       sync.Once
	allocCtx   context.Context
	allocCxl   context.CancelFunc
	configured string // exec path set via Configure, "" = autodetect
)

// Configure sets the Chrome executable path used for the lazily-created
// browser. Must be called before the first Render. Empty string = let
// chromedp autodetect a system Chrome/Chromium.
func Configure(execPath string) {
	configured = execPath
}

// Render navigates to url headless and returns the fully rendered outer HTML
// of the document. The browser process is shared across calls (created on
// first use); each call opens its own tab.
func Render(ctx context.Context, url string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	ensureBrowser()

	cctx, cancel := context.WithTimeout(allocCtx, timeout)
	defer cancel()
	taskCtx, cancel := chromedp.NewContext(cctx)
	defer cancel()

	var html string
	if err := chromedp.Run(taskCtx,
		chromedp.Navigate(url),
		chromedp.WaitReady("body", chromedp.ByQuery),
		chromedp.Sleep(settleAfterLoad),
		chromedp.OuterHTML("html", &html, chromedp.ByQuery),
	); err != nil {
		return "", fmt.Errorf("render %s: %w", url, err)
	}
	return html, nil
}

// ensureBrowser lazily creates the shared exec allocator + browser once.
func ensureBrowser() {
	once.Do(func() {
		opts := append([]chromedp.ExecAllocatorOption{},
			chromedp.DefaultExecAllocatorOptions[:]...,
		)
		if configured != "" {
			opts = append(opts, chromedp.ExecPath(configured))
		}
		allocCtx, allocCxl = chromedp.NewExecAllocator(context.Background(), opts...)
	})
}

// Close shuts down the shared browser process. Safe to call multiple times;
// a no-op if Render was never called.
func Close() {
	if allocCxl != nil {
		allocCxl()
		allocCxl = nil
	}
}