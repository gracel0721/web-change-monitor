// Package scheduler is the long-running tick loop that wakes the worker to
// check due websites on their configured cadence.
package scheduler

import (
	"context"
	"log"
	"time"

	"webwatch/internal/worker"
)

// Scheduler drives periodic checks. Tick every `interval` (default 30s), and on
// each tick ask the worker to check whatever is due.
type Scheduler struct {
	Worker   *worker.Worker
	Interval time.Duration
}

// New constructs a scheduler with a 30s default tick interval.
func New(w *worker.Worker) *Scheduler {
	return &Scheduler{Worker: w, Interval: 30 * time.Second}
}

// Run blocks until ctx is cancelled, ticking on the configured interval.
func (s *Scheduler) Run(ctx context.Context) {
	ticker := time.NewTicker(s.Interval)
	defer ticker.Stop()

	log.Printf("scheduler started; tick interval %s", s.Interval)
	// Run once immediately so a fresh daemon doesn't wait a tick.
	s.tick(ctx)

	for {
		select {
		case <-ctx.Done():
			log.Printf("scheduler stopped")
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	results := s.Worker.CheckAll(ctx)
	for _, r := range results {
		switch r.Status {
		case "error":
			if r.Website != nil {
				log.Printf("check error: %s: %v", r.Website.URL, r.Err)
			} else {
				log.Printf("check error: %v", r.Err)
			}
		case "changed":
			// notification already printed by the worker
		}
	}
}