// Package cli implements the webwatch command tree (cobra).
package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"webwatch/internal/config"
	"webwatch/internal/db"
	"webwatch/internal/render"
	"webwatch/internal/scheduler"
	"webwatch/internal/worker"
)

// Root builds the root command and all subcommands.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "webwatch",
		Short: "Monitor web pages for changes, with LLM-generated summaries.",
	}
	var dbPath string
	root.PersistentFlags().StringVar(&dbPath, "db", "", "path to the SQLite database (default ~/.webwatch/webwatch.db)")

	resolveDB := func() (*db.Store, error) {
		p := dbPath
		if p == "" {
			dp, err := config.DBPath()
			if err != nil {
				return nil, err
			}
			p = dp
		}
		return db.Open(p)
	}

	root.AddCommand(
		addCmd(resolveDB),
		listCmd(resolveDB),
		removeCmd(resolveDB),
		runCmd(resolveDB),
		startCmd(resolveDB),
		changesCmd(resolveDB),
		configCmd(),
	)
	return root
}

// ---- helpers ----

func mustStore(open func() (*db.Store, error)) *db.Store {
	s, err := open()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error opening database:", err)
		os.Exit(1)
	}
	return s
}

func parseFreq(s string) (int, error) {
	if s == "" {
		return 3600, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid --freq %q: %w", s, err)
	}
	secs := int(d.Seconds())
	if secs < 1 {
		secs = 1
	}
	return secs, nil
}

// humanize renders a *time.Time as "2 hours ago" / "just now" / "never".
func humanize(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}

func writeTable(w io.Writer, header []string, rows [][]string) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	if len(header) > 0 {
		fmt.Fprintln(tw, strings.Join(header, "\t"))
	}
	for _, r := range rows {
		fmt.Fprintln(tw, strings.Join(r, "\t"))
	}
	tw.Flush()
}

// ---- add ----

func addCmd(open func() (*db.Store, error)) *cobra.Command {
	var name, freq string
	var js bool
	c := &cobra.Command{
		Use:   "add <url>",
		Short: "Track a new URL and run an immediate first check.",
		Args:  cobra.ExactArgs(1),
	}
	c.Flags().StringVar(&name, "name", "", "human-friendly label")
	c.Flags().StringVar(&freq, "freq", "1h", "check frequency (e.g. 30m, 1h, 24h)")
	c.Flags().BoolVar(&js, "js", false, "render the page with a headless browser (for JS-heavy sites)")
	c.Run = func(cmd *cobra.Command, args []string) {
		url := args[0]
		secs, err := parseFreq(freq)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		store := mustStore(open)
		defer store.Close()

		site, err := store.AddWebsite(url, name, secs, js)
		if err != nil {
			if err == db.ErrDuplicate {
				fmt.Fprintln(os.Stderr, "that URL is already tracked")
				os.Exit(1)
			}
			fmt.Fprintln(os.Stderr, "add:", err)
			os.Exit(1)
		}
		fmt.Printf("Added: id=%d  url=%s  freq=%s  js=%v\n", site.ID, site.URL, freq, site.RenderJS)

		// Seed the baseline snapshot immediately so the first real check has
		// something to diff against.
		settings, _ := config.Load()
		w := worker.New(store, settings)
		if err := w.ResolveScript(); err != nil {
			// LLM not strictly needed for seeding; just warn.
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
		res := w.Check(context.Background(), site.ID)
		if res.Err != nil {
			fmt.Fprintln(os.Stderr, "initial check failed:", res.Err)
		} else {
			fmt.Printf("Baseline snapshot saved (status=%s)\n", res.Status)
		}
	}
	return c
}

// ---- list ----

func listCmd(open func() (*db.Store, error)) *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "List tracked websites and when each was last checked.",
		Args:  cobra.NoArgs,
	}
	c.Run = func(cmd *cobra.Command, args []string) {
		store := mustStore(open)
		defer store.Close()
		sites, err := store.ListWebsites()
		if err != nil {
			fmt.Fprintln(os.Stderr, "list:", err)
			os.Exit(1)
		}
		if len(sites) == 0 {
			fmt.Println("No websites tracked. Use 'webwatch add <url>' to start.")
			return
		}
		var rows [][]string
		for _, s := range sites {
			rows = append(rows, []string{
				strconv.FormatInt(s.ID, 10),
				s.URL,
				humanize(s.LastCheckedAt),
				statusDisplay(s.LastStatus),
			})
		}
		writeTable(os.Stdout, []string{"ID", "URL", "LAST CHECK", "STATUS"}, rows)
	}
	return c
}

func statusDisplay(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// ---- remove ----

func removeCmd(open func() (*db.Store, error)) *cobra.Command {
	c := &cobra.Command{
		Use:   "remove <id>",
		Short: "Stop tracking a website and delete its history.",
		Args:  cobra.ExactArgs(1),
	}
	c.Run = func(cmd *cobra.Command, args []string) {
		id, err := strconv.ParseInt(args[0], 10, 64)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid id:", args[0])
			os.Exit(1)
		}
		store := mustStore(open)
		defer store.Close()
		if err := store.DeleteWebsite(id); err != nil {
			fmt.Fprintln(os.Stderr, "remove:", err)
			os.Exit(1)
		}
		fmt.Printf("Removed id=%d\n", id)
	}
	return c
}

// ---- run ----

func runCmd(open func() (*db.Store, error)) *cobra.Command {
	var id int64
	c := &cobra.Command{
		Use:   "run",
		Short: "Run one check pass (all due sites, or just --id).",
		Args:  cobra.NoArgs,
	}
	c.Flags().Int64Var(&id, "id", 0, "check only this website id")
	c.Run = func(cmd *cobra.Command, args []string) {
		store := mustStore(open)
		defer store.Close()
		settings, _ := config.Load()
		w := worker.New(store, settings)
		if err := w.ResolveScript(); err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
		ctx := context.Background()
		if id != 0 {
			res := w.Check(ctx, id)
			printRunResult(res)
			if res.Err != nil {
				os.Exit(1)
			}
			return
		}
		for _, res := range w.CheckAll(ctx) {
			printRunResult(res)
		}
	}
	return c
}

func printRunResult(r worker.CheckResult) {
	if r.Website == nil {
		fmt.Fprintln(os.Stderr, "check error:", r.Err)
		return
	}
	if r.Err != nil {
		fmt.Fprintf(os.Stderr, "[%d] %s: error: %v\n", r.Website.ID, r.Website.URL, r.Err)
		return
	}
	fmt.Printf("[%d] %s: %s\n", r.Website.ID, r.Website.URL, r.Status)
}

// ---- start ----

func startCmd(open func() (*db.Store, error)) *cobra.Command {
	var interval string
	c := &cobra.Command{
		Use:   "start",
		Short: "Run the scheduler daemon until interrupted (Ctrl-C).",
		Args:  cobra.NoArgs,
	}
	c.Flags().StringVar(&interval, "interval", "30s", "scheduler tick interval")
	c.Run = func(cmd *cobra.Command, args []string) {
		store := mustStore(open)
		defer store.Close()
		settings, _ := config.Load()
		w := worker.New(store, settings)
		if err := w.ResolveScript(); err != nil {
			fmt.Fprintln(os.Stderr, "warning:", err)
		}
		d, err := time.ParseDuration(interval)
		if err != nil {
			fmt.Fprintln(os.Stderr, "invalid --interval:", err)
			os.Exit(1)
		}
		sched := scheduler.New(w)
		sched.Interval = d

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		// Ctrl-C cancels the context, which stops the scheduler cleanly.
		setupSignal(cancel)
		sched.Run(ctx)
		// Release the shared headless browser (if one was started) on exit.
		render.Close()
	}
	return c
}

// ---- changes ----

func changesCmd(open func() (*db.Store, error)) *cobra.Command {
	var id int64
	var limit int
	c := &cobra.Command{
		Use:   "changes",
		Short: "Show detected changes.",
		Args:  cobra.NoArgs,
	}
	c.Flags().Int64Var(&id, "id", 0, "filter to this website id")
	c.Flags().IntVar(&limit, "limit", 20, "max changes to show")
	c.Run = func(cmd *cobra.Command, args []string) {
		store := mustStore(open)
		defer store.Close()
		changes, err := store.ListChanges(id, limit)
		if err != nil {
			fmt.Fprintln(os.Stderr, "changes:", err)
			os.Exit(1)
		}
		if len(changes) == 0 {
			fmt.Println("No changes recorded yet.")
			return
		}
		for _, ch := range changes {
			site, _ := store.GetWebsite(ch.WebsiteID)
			var oldContent, newContent string
			if ch.OldSnapshotID != nil {
				if sn, err := store.GetSnapshot(*ch.OldSnapshotID); err == nil {
					oldContent = sn.Content
				}
			}
			if sn, err := store.GetSnapshot(ch.NewSnapshotID); err == nil {
				newContent = sn.Content
			}
			summary := ""
			if ch.Summary != nil {
				summary = *ch.Summary
			}
			url := ""
			name := ""
			if site != nil {
				url = site.URL
				name = site.Name
			}
			printChangeBlock(os.Stdout, url, name, oldContent, newContent, ch.Diff, summary, ch.Importance, ch.CreatedAt)
		}
	}
	return c
}

func printChangeBlock(w io.Writer, url, name, oldContent, newContent, diff, summary, importance string, at time.Time) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "🚨 CHANGE DETECTED")
	fmt.Fprintln(w)
	if url == "" {
		url = "(unknown url)"
	}
	fmt.Fprintln(w, url)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Before:")
	fmt.Fprintln(w, indentBlock(oldContent, 2))
	fmt.Fprintln(w)
	fmt.Fprintln(w, "After:")
	fmt.Fprintln(w, indentBlock(newContent, 2))
	fmt.Fprintln(w)
	if summary != "" {
		fmt.Fprintln(w, "AI SUMMARY:")
		fmt.Fprintln(w, summary)
		fmt.Fprintln(w)
	} else {
		fmt.Fprintln(w, "AI SUMMARY: (unavailable — LLM failed or not configured)")
		fmt.Fprintln(w)
	}
	imp := strings.ToUpper(importance)
	if imp == "" {
		imp = "UNKNOWN"
	}
	fmt.Fprintln(w, "Importance: "+imp)
	fmt.Fprintln(w, "When:", at.Format(time.RFC3339))
	fmt.Fprintln(w)
}

func indentBlock(s string, n int) string {
	if s == "" {
		return strings.Repeat(" ", n) + "(none)"
	}
	pad := strings.Repeat(" ", n)
	const max = 800
	if len(s) > max {
		s = s[:max] + "\n[...truncated...]"
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	for i, l := range lines {
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

// ---- config ----

func configCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "config [key value]",
		Short: "Show or set settings (ollama_url, ollama_model, webhook_url).",
		Args:  cobra.RangeArgs(0, 2),
	}
	c.Run = func(cmd *cobra.Command, args []string) {
		settings, err := config.Load()
		if err != nil {
			fmt.Fprintln(os.Stderr, "load config:", err)
			os.Exit(1)
		}
		if len(args) == 0 {
			fmt.Printf("ollama_url:   %s\n", settings.OllamaURL)
			fmt.Printf("ollama_model: %s\n", settings.OllamaModel)
			fmt.Printf("webhook_url:  %s\n", settings.WebhookURL)
			chrome := settings.ChromePath
			if chrome == "" {
				chrome = "(autodetect)"
			}
			fmt.Printf("chrome_path:  %s\n", chrome)
			return
		}
		if len(args) == 1 {
			fmt.Fprintln(os.Stderr, "usage: webwatch config <key> <value>")
			os.Exit(1)
		}
		key, val := args[0], args[1]
		switch key {
		case "ollama_url":
			settings.OllamaURL = val
		case "ollama_model":
			settings.OllamaModel = val
		case "webhook_url":
			settings.WebhookURL = val
		case "chrome_path":
			settings.ChromePath = val
		default:
			fmt.Fprintln(os.Stderr, "unknown key:", key)
			fmt.Fprintln(os.Stderr, "valid keys: ollama_url, ollama_model, webhook_url, chrome_path")
			os.Exit(1)
		}
		if err := config.Save(settings); err != nil {
			fmt.Fprintln(os.Stderr, "save config:", err)
			os.Exit(1)
		}
		fmt.Printf("set %s = %s\n", key, val)
	}
	return c
}