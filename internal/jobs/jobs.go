// Package jobs runs the web server's background work: trench fan-out,
// notifications and cleanup after deleted accounts. Fan-out and cleanup
// interrupted by a deploy are picked up again, because pending work is
// recorded in DynamoDB, not only in memory. Notifications
// are best effort: one lost in a restart isn't worth a durable queue.
package jobs

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/t0ul/krabber-net/internal/store"
)

const (
	fanoutQueueSize   = 256
	fanoutSweepEvery  = time.Minute
	fanoutSweepMinAge = 30 * time.Second // leave fresh molts to the in-process worker
	fanoutSweepBatch  = 25
	notifyQueueSize   = 512
	purgeSweepEvery   = time.Minute
	purgeSweepBatch   = 5
)

// Runner owns the background goroutines.
type Runner struct {
	store *store.Store
	log   *slog.Logger
	queue chan *store.Molt
	notes chan store.Notification
	wg    sync.WaitGroup
}

// New returns a Runner; call Start to begin work.
func New(s *store.Store, log *slog.Logger) *Runner {
	return &Runner{
		store: s,
		log:   log,
		queue: make(chan *store.Molt, fanoutQueueSize),
		notes: make(chan store.Notification, notifyQueueSize),
	}
}

// Notify hands a notification to the writer without blocking the request.
func (r *Runner) Notify(n store.Notification) {
	select {
	case r.notes <- n:
	default:
		r.log.Warn("notification queue full; dropping", "type", n.Type, "recipient", n.RecipientID)
	}
}

// Enqueue hands a new molt to the fan-out worker without blocking the
// request. If the queue is full, the sweep picks the molt up within a minute.
func (r *Runner) Enqueue(m *store.Molt) {
	select {
	case r.queue <- m:
	default:
		r.log.Warn("fanout queue full; leaving molt for the sweep", "molt", m.ID)
	}
}

// Start launches the workers. They stop when ctx is cancelled.
func (r *Runner) Start(ctx context.Context) {
	r.wg.Add(4)
	go r.fanoutWorker(ctx)
	go r.notifyWorker(ctx)
	go r.every(ctx, fanoutSweepEvery, 5*time.Second, r.sweepFanouts)
	go r.every(ctx, purgeSweepEvery, 15*time.Second, r.sweepPurges)
}

// sweepPurges cleans up after deleted accounts. A purge cut short by a
// deploy stays queued and runs again from the start.
func (r *Runner) sweepPurges(ctx context.Context) {
	ids, err := r.store.PendingPurges(ctx, purgeSweepBatch)
	if err != nil {
		r.log.Error("purge sweep failed", "err", err)
		return
	}
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()
		if err := r.store.PurgeCrab(ctx, id); err != nil {
			if ctx.Err() == nil {
				r.log.Error("purge failed; will retry", "crab", id, "err", err)
			}
			continue
		}
		r.log.Info("purge done", "crab", id, "ms", time.Since(start).Milliseconds())
	}
}

func (r *Runner) notifyWorker(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case n := <-r.notes:
			nctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := r.store.AddNotification(nctx, n); err != nil {
				r.log.Error("notification failed", "type", n.Type, "recipient", n.RecipientID, "err", err)
			}
			cancel()
		}
	}
}

// Wait blocks until the workers exit or timeout passes. Unfinished fan-out
// stays marked pending in the table.
func (r *Runner) Wait(timeout time.Duration) {
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
		r.log.Warn("background jobs still running at shutdown; pending work will be resumed")
	}
}

func (r *Runner) fanoutWorker(ctx context.Context) {
	defer r.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-r.queue:
			// Finish a started fan-out even if shutdown begins meanwhile.
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			r.fanout(fctx, m)
			cancel()
		}
	}
}

func (r *Runner) sweepFanouts(ctx context.Context) {
	molts, err := r.store.PendingFanouts(ctx, time.Now().Add(-fanoutSweepMinAge), fanoutSweepBatch)
	if err != nil {
		r.log.Error("fanout sweep failed", "err", err)
		return
	}
	for i := range molts {
		if ctx.Err() != nil {
			return
		}
		r.fanout(ctx, &molts[i])
	}
}

// fanout writes the molt into the owner's own trench and every follower's,
// then clears its pending marker. On failure the marker stays and the sweep
// retries; the writes are idempotent.
func (r *Runner) fanout(ctx context.Context, m *store.Molt) {
	start := time.Now()
	followers := 0
	err := r.store.AddToTrenches(ctx, m, []string{m.OwnerID})
	if err == nil {
		err = r.store.EachFollowerPage(ctx, m.OwnerID, func(ids []string) error {
			followers += len(ids)
			return r.store.AddToTrenches(ctx, m, ids)
		})
	}
	if err == nil {
		err = r.store.ClearFanout(ctx, m)
	}
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("fanout failed; will retry", "molt", m.ID, "err", err)
		}
		return
	}
	r.log.Info("fanout done", "molt", m.ID, "followers", followers, "ms", time.Since(start).Milliseconds())
}

// every runs fn after an initial delay and then on each tick, with up to 10%
// jitter so several instances don't run in lockstep.
func (r *Runner) every(ctx context.Context, interval, initial time.Duration, fn func(context.Context)) {
	defer r.wg.Done()
	timer := time.NewTimer(initial)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			fn(ctx)
			jitter := time.Duration(rand.Int64N(int64(interval) / 10)) //nolint:gosec // scheduling jitter
			timer.Reset(interval + jitter)
		}
	}
}
