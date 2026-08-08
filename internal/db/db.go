// Package db is the persistence layer for webwatch. It owns the SQLite
// connection, schema migrations, and all queries the rest of the app uses.
package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, registers "sqlite"
)

// Store wraps a *sql.DB plus its configured location.
type Store struct {
	*sql.DB
	path string
}

// Website is a tracked page.
type Website struct {
	ID                     int64
	URL                    string
	Name                   string
	CreatedAt              time.Time
	CheckFrequencySeconds  int
	LastCheckedAt          *time.Time
	NextCheckAt            *time.Time
	LastStatus             string // "unchanged" | "changed" | "error" | "new"
	LastError              string
	RenderJS               bool // true → render via headless Chrome before diffing
}

// Snapshot is one normalized capture of a page.
type Snapshot struct {
	ID          int64
	WebsiteID   int64
	ContentHash string
	Content     string
	CreatedAt   time.Time
}

// Change is a detected difference between two snapshots.
type Change struct {
	ID            int64
	WebsiteID     int64
	OldSnapshotID *int64 // nil for the very first "change" (old vs new)
	NewSnapshotID int64
	Diff          string
	Summary       *string // nil when LLM summarization failed/skipped
	Importance    string  // "LOW" | "MEDIUM" | "HIGH" | ""
	CreatedAt     time.Time
}

// Open opens (creating if needed) the SQLite database at path and runs
// migrations. It is safe to call repeatedly.
func Open(path string) (*Store, error) {
	sqlDB, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite handles concurrency best with one connection for writes; the Go
	// database/sql pool multiplexes over it.
	if _, err := sqlDB.Exec("PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("set pragmas: %w", err)
	}
	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	s := &Store{DB: sqlDB, path: path}
	if err := s.migrate(); err != nil {
		sqlDB.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS websites (
			id                      INTEGER PRIMARY KEY AUTOINCREMENT,
			url                     TEXT NOT NULL UNIQUE,
			name                    TEXT NOT NULL DEFAULT '',
			created_at              DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			check_frequency_seconds INTEGER NOT NULL DEFAULT 3600,
			last_checked_at         DATETIME,
			next_check_at           DATETIME,
			last_status              TEXT NOT NULL DEFAULT '',
			last_error               TEXT NOT NULL DEFAULT '',
			render_js               INTEGER NOT NULL DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS snapshots (
			id           INTEGER PRIMARY KEY AUTOINCREMENT,
			website_id   INTEGER NOT NULL REFERENCES websites(id) ON DELETE CASCADE,
			content_hash TEXT NOT NULL,
			content      TEXT NOT NULL,
			created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_snapshots_website ON snapshots(website_id, created_at)`,
		`CREATE TABLE IF NOT EXISTS changes (
			id              INTEGER PRIMARY KEY AUTOINCREMENT,
			website_id      INTEGER NOT NULL REFERENCES websites(id) ON DELETE CASCADE,
			old_snapshot_id INTEGER REFERENCES snapshots(id) ON DELETE SET NULL,
			new_snapshot_id INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
			diff            TEXT NOT NULL DEFAULT '',
			summary         TEXT,
			importance      TEXT NOT NULL DEFAULT '',
			created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE INDEX IF NOT EXISTS idx_changes_website ON changes(website_id, created_at)`,
	}
	for _, q := range stmts {
		if _, err := s.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	// Additive migration for existing databases: add render_js if absent.
	if !s.columnExists("websites", "render_js") {
		if _, err := s.Exec(`ALTER TABLE websites ADD COLUMN render_js INTEGER NOT NULL DEFAULT 0`); err != nil {
			return fmt.Errorf("migrate render_js: %w", err)
		}
	}
	return nil
}

// columnExists reports whether a column exists on a table.
func (s *Store) columnExists(table, column string) bool {
	rows, err := s.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false
		}
		if name == column {
			return true
		}
	}
	return false
}

// AddWebsite inserts a website and returns it. Returns ErrDuplicate if the URL
// is already tracked.
func (s *Store) AddWebsite(url, name string, freqSeconds int, renderJS bool) (*Website, error) {
	jsVal := 0
	if renderJS {
		jsVal = 1
	}
	res, err := s.Exec(
		`INSERT INTO websites (url, name, check_frequency_seconds, render_js) VALUES (?, ?, ?, ?)`,
		url, name, freqSeconds, jsVal,
	)
	if err != nil {
		// modernc/sqlite returns an error whose message contains "UNIQUE"; detect that.
		if isUniqueViolation(err) {
			return nil, ErrDuplicate
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetWebsite(id)
}

// ErrDuplicate is returned when a URL is already tracked.
var ErrDuplicate = errors.New("url already tracked")

func isUniqueViolation(err error) bool {
	return err != nil && contains(err.Error(), "UNIQUE")
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// GetWebsite fetches a single website by id.
func (s *Store) GetWebsite(id int64) (*Website, error) {
	w := &Website{}
	var lcb, ncb sql.NullTime
	err := s.QueryRow(
		`SELECT id, url, name, created_at, check_frequency_seconds,
		        last_checked_at, next_check_at, last_status, last_error, render_js
		 FROM websites WHERE id = ?`, id,
	).Scan(&w.ID, &w.URL, &w.Name, &w.CreatedAt, &w.CheckFrequencySeconds,
		&lcb, &ncb, &w.LastStatus, &w.LastError, &w.RenderJS)
	if err != nil {
		return nil, err
	}
	if lcb.Valid {
		w.LastCheckedAt = &lcb.Time
	}
	if ncb.Valid {
		w.NextCheckAt = &ncb.Time
	}
	return w, nil
}

// GetWebsiteByURL fetches a single website by url.
func (s *Store) GetWebsiteByURL(url string) (*Website, error) {
	w := &Website{}
	var lcb, ncb sql.NullTime
	err := s.QueryRow(
		`SELECT id, url, name, created_at, check_frequency_seconds,
		        last_checked_at, next_check_at, last_status, last_error, render_js
		 FROM websites WHERE url = ?`, url,
	).Scan(&w.ID, &w.URL, &w.Name, &w.CreatedAt, &w.CheckFrequencySeconds,
		&lcb, &ncb, &w.LastStatus, &w.LastError, &w.RenderJS)
	if err != nil {
		return nil, err
	}
	if lcb.Valid {
		w.LastCheckedAt = &lcb.Time
	}
	if ncb.Valid {
		w.NextCheckAt = &ncb.Time
	}
	return w, nil
}

// ListWebsites returns all tracked websites, ordered by id.
func (s *Store) ListWebsites() ([]*Website, error) {
	rows, err := s.Query(
		`SELECT id, url, name, created_at, check_frequency_seconds,
		        last_checked_at, next_check_at, last_status, last_error, render_js
		 FROM websites ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Website
	for rows.Next() {
		w := &Website{}
		var lcb, ncb sql.NullTime
		if err := rows.Scan(&w.ID, &w.URL, &w.Name, &w.CreatedAt, &w.CheckFrequencySeconds,
			&lcb, &ncb, &w.LastStatus, &w.LastError, &w.RenderJS); err != nil {
			return nil, err
		}
		if lcb.Valid {
			w.LastCheckedAt = &lcb.Time
		}
		if ncb.Valid {
			w.NextCheckAt = &ncb.Time
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// DeleteWebsite removes a website and its snapshots/changes (cascaded).
func (s *Store) DeleteWebsite(id int64) error {
	_, err := s.Exec(`DELETE FROM websites WHERE id = ?`, id)
	return err
}

// LatestSnapshot returns the most recent snapshot for a website, or
// sql.ErrNoRows if none exists.
func (s *Store) LatestSnapshot(websiteID int64) (*Snapshot, error) {
	sn := &Snapshot{}
	err := s.QueryRow(
		`SELECT id, website_id, content_hash, content, created_at
		 FROM snapshots WHERE website_id = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		websiteID,
	).Scan(&sn.ID, &sn.WebsiteID, &sn.ContentHash, &sn.Content, &sn.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sn, nil
}

// InsertSnapshot stores a new snapshot and returns it.
func (s *Store) InsertSnapshot(websiteID int64, contentHash, content string) (*Snapshot, error) {
	res, err := s.Exec(
		`INSERT INTO snapshots (website_id, content_hash, content) VALUES (?, ?, ?)`,
		websiteID, contentHash, content)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &Snapshot{ID: id, WebsiteID: websiteID, ContentHash: contentHash, Content: content, CreatedAt: time.Now()}, nil
}

// InsertChange stores a detected change.
func (s *Store) InsertChange(websiteID int64, oldID *int64, newID int64, diff, summary, importance string) (*Change, error) {
	var summaryArg interface{}
	if summary == "" {
		summaryArg = nil
	} else {
		summaryArg = summary
	}
	res, err := s.Exec(
		`INSERT INTO changes (website_id, old_snapshot_id, new_snapshot_id, diff, summary, importance)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		websiteID, oldID, newID, diff, summaryArg, importance)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	c := &Change{ID: id, WebsiteID: websiteID, OldSnapshotID: oldID, NewSnapshotID: newID, Diff: diff, Importance: importance, CreatedAt: time.Now()}
	if summary != "" {
		s := summary
		c.Summary = &s
	}
	return c, nil
}

// MarkChecked records the result of a check against a website.
func (s *Store) MarkChecked(id int64, status, lastError string, nextCheckAt time.Time) error {
	_, err := s.Exec(
		`UPDATE websites SET last_checked_at = CURRENT_TIMESTAMP, next_check_at = ?, last_status = ?, last_error = ? WHERE id = ?`,
		nextCheckAt, status, lastError, id)
	return err
}

// ListChanges returns changes, newest first, optionally filtered to a website.
func (s *Store) ListChanges(websiteID int64, limit int) ([]*Change, error) {
	if limit <= 0 {
		limit = 50
	}
	var rows *sql.Rows
	var err error
	if websiteID > 0 {
		rows, err = s.Query(
			`SELECT id, website_id, old_snapshot_id, new_snapshot_id, diff, summary, importance, created_at
			 FROM changes WHERE website_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`,
			websiteID, limit)
	} else {
		rows, err = s.Query(
			`SELECT id, website_id, old_snapshot_id, new_snapshot_id, diff, summary, importance, created_at
			 FROM changes ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Change
	for rows.Next() {
		c := &Change{}
		var oldID sql.NullInt64
		var summary sql.NullString
		if err := rows.Scan(&c.ID, &c.WebsiteID, &oldID, &c.NewSnapshotID, &c.Diff, &summary, &c.Importance, &c.CreatedAt); err != nil {
			return nil, err
		}
		if oldID.Valid {
			oi := oldID.Int64
			c.OldSnapshotID = &oi
		}
		if summary.Valid {
			s := summary.String
			c.Summary = &s
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetSnapshot fetches a snapshot by id.
func (s *Store) GetSnapshot(id int64) (*Snapshot, error) {
	sn := &Snapshot{}
	err := s.QueryRow(
		`SELECT id, website_id, content_hash, content, created_at FROM snapshots WHERE id = ?`, id,
	).Scan(&sn.ID, &sn.WebsiteID, &sn.ContentHash, &sn.Content, &sn.CreatedAt)
	if err != nil {
		return nil, err
	}
	return sn, nil
}

// DueWebsites returns websites whose next_check_at is at or before now, or
// which have never been checked.
func (s *Store) DueWebsites(now time.Time) ([]*Website, error) {
	rows, err := s.Query(
		`SELECT id, url, name, created_at, check_frequency_seconds,
		        last_checked_at, next_check_at, last_status, last_error, render_js
		 FROM websites
		 WHERE next_check_at IS NULL OR next_check_at <= ?
		 ORDER BY id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Website
	for rows.Next() {
		w := &Website{}
		var lcb, ncb sql.NullTime
		if err := rows.Scan(&w.ID, &w.URL, &w.Name, &w.CreatedAt, &w.CheckFrequencySeconds,
			&lcb, &ncb, &w.LastStatus, &w.LastError, &w.RenderJS); err != nil {
			return nil, err
		}
		if lcb.Valid {
			w.LastCheckedAt = &lcb.Time
		}
		if ncb.Valid {
			w.NextCheckAt = &ncb.Time
		}
		out = append(out, w)
	}
	return out, rows.Err()
}