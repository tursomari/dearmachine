package client

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type messageWork struct {
	message    Message
	pending    PendingMessage
	recovering bool
}

type threadWorkQueue struct {
	mu        sync.Mutex
	items     []messageWork
	inFlight  map[string]struct{}
	active    map[string]messageWork
	messageID map[string]struct{}
	added     chan struct{}
}

func newThreadWorkQueue() *threadWorkQueue {
	return &threadWorkQueue{
		inFlight:  make(map[string]struct{}),
		active:    make(map[string]messageWork),
		messageID: make(map[string]struct{}),
		added:     make(chan struct{}, 1),
	}
}

func (q *threadWorkQueue) enqueue(work messageWork) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if _, exists := q.messageID[work.pending.MessageID]; exists {
		return
	}
	q.messageID[work.pending.MessageID] = struct{}{}
	q.items = append(q.items, work)
	select {
	case q.added <- struct{}{}:
	default:
	}
}

func (q *threadWorkQueue) take() (messageWork, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Scan in enqueue order so concurrency one remains globally FIFO. A busy
	// thread stays queued while later independent threads can use other workers.
	for index, work := range q.items {
		threadID := work.pending.ThreadID
		if _, busy := q.inFlight[threadID]; busy {
			continue
		}
		q.inFlight[threadID] = struct{}{}
		q.active[threadID] = work
		q.items = append(q.items[:index], q.items[index+1:]...)
		return work, true
	}
	return messageWork{}, false
}

func (q *threadWorkQueue) finish(threadID string) {
	q.mu.Lock()
	delete(q.inFlight, threadID)
	delete(q.active, threadID)
	q.mu.Unlock()
}

func (q *threadWorkQueue) inFlightWork(threadID string) (messageWork, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	work, ok := q.active[threadID]
	return work, ok
}

func (q *threadWorkQueue) supersededActive() []messageWork {
	q.mu.Lock()
	defer q.mu.Unlock()
	seen := make(map[string]struct{})
	var superseded []messageWork
	for _, queued := range q.items {
		threadID := queued.pending.ThreadID
		if _, found := seen[threadID]; found {
			continue
		}
		active, found := q.active[threadID]
		if !found || queued.pending.Session.Sequence <= active.pending.Session.Sequence {
			continue
		}
		seen[threadID] = struct{}{}
		superseded = append(superseded, active)
	}
	return superseded
}

type workResult struct {
	work messageWork
	err  error
}

type workStarted struct {
	work messageWork
	at   time.Time
}

func (a *App) preemptDue(
	work *threadWorkQueue,
	startedAt map[string]time.Time,
	resolved map[string]struct{},
) error {
	now := a.preemptionNow()
	for _, active := range work.supersededActive() {
		messageID := active.pending.MessageID
		if _, found := resolved[messageID]; found {
			continue
		}
		started, found := startedAt[messageID]
		if !found || now.Before(started.Add(a.pollInterval)) {
			continue
		}
		// Stop and command release are serialized by AgentRunner's active lock.
		// A false result means the run completed before preemption won.
		resolved[messageID] = struct{}{}
		if !a.runner.Stop(active.pending.ThreadID) {
			continue
		}
		if _, err := a.store.SkipPreemptedMessage(
			MessageRef{MessageID: messageID, ThreadID: active.pending.ThreadID},
			"preempted by newer email",
		); err != nil {
			return fmt.Errorf("skip preempted message %s: %w", messageID, err)
		}
		if a.verbose {
			a.logger.Printf(
				"preempted message=%s thread=%s after grace=%s",
				messageID,
				active.pending.ThreadID,
				a.pollInterval,
			)
		}
	}
	return nil
}

func (a *App) nextPreemption(
	work *threadWorkQueue,
	startedAt map[string]time.Time,
	resolved map[string]struct{},
) <-chan time.Time {
	now := a.preemptionNow()
	var earliest time.Time
	for _, active := range work.supersededActive() {
		messageID := active.pending.MessageID
		if _, found := resolved[messageID]; found {
			continue
		}
		started, found := startedAt[messageID]
		if !found {
			continue
		}
		deadline := started.Add(a.pollInterval)
		if earliest.IsZero() || deadline.Before(earliest) {
			earliest = deadline
		}
	}
	if earliest.IsZero() {
		return nil
	}
	delay := earliest.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return a.preemptionAfter(delay)
}

func (a *App) dispatch(ctx context.Context, work *threadWorkQueue) error {
	jobs := make(chan messageWork)
	results := make(chan workResult, a.concurrency)
	started := make(chan workStarted, a.concurrency)
	var workers sync.WaitGroup
	workers.Add(a.concurrency)
	for range a.concurrency {
		go func() {
			defer workers.Done()
			for item := range jobs {
				if a.workerGate != nil {
					select {
					case a.workerGate <- struct{}{}:
					case <-ctx.Done():
						results <- workResult{work: item, err: ctx.Err()}
						continue
					}
				}
				result := a.processWork(ctx, item, func() {
					started <- workStarted{work: item, at: a.preemptionNow()}
				})
				if a.workerGate != nil {
					<-a.workerGate
				}
				results <- workResult{work: item, err: result}
			}
		}()
	}

	stopClaims := make(chan struct{})
	var claimLoop sync.WaitGroup
	var claimErrMu sync.Mutex
	var claimErr error
	claimLoop.Add(1)
	go func() {
		defer claimLoop.Done()
		ticker := time.NewTicker(a.pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stopClaims:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := a.pollAndClaim(ctx, work); err != nil {
					claimErrMu.Lock()
					claimErr = err
					claimErrMu.Unlock()
					return
				}
			}
		}
	}()

	active := 0
	var firstErr error
	claimsStopped := false
	startedAt := make(map[string]time.Time)
	resolved := make(map[string]struct{})
	for {
		if firstErr == nil {
			if err := a.preemptDue(work, startedAt, resolved); err != nil {
				firstErr = err
			}
		}
		for firstErr == nil && active < a.concurrency {
			item, ok := work.take()
			if !ok {
				break
			}
			jobs <- item
			active++
		}
		if active == 0 {
			if !claimsStopped {
				close(stopClaims)
				claimLoop.Wait()
				claimsStopped = true
				continue
			}
			break
		}

		preemption := a.nextPreemption(work, startedAt, resolved)
		select {
		case result := <-results:
			active--
			delete(startedAt, result.work.pending.MessageID)
			delete(resolved, result.work.pending.MessageID)
			work.finish(result.work.pending.ThreadID)
			if result.err != nil && firstErr == nil {
				operation := "process"
				if result.work.recovering {
					operation = "recover"
				}
				firstErr = fmt.Errorf(
					"%s message %s: %w",
					operation,
					result.work.pending.MessageID,
					result.err,
				)
			}
		case event := <-started:
			activeWork, found := work.inFlightWork(event.work.pending.ThreadID)
			if found && activeWork.pending.MessageID == event.work.pending.MessageID {
				startedAt[event.work.pending.MessageID] = event.at
			}
		case <-work.added:
		case <-preemption:
		}
	}

	if !claimsStopped {
		close(stopClaims)
		claimLoop.Wait()
	}
	claimErrMu.Lock()
	if claimErr != nil && firstErr == nil {
		firstErr = claimErr
	}
	claimErrMu.Unlock()

	close(jobs)
	workers.Wait()
	return firstErr
}
