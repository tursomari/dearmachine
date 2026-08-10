package deviceclient

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
	messageID map[string]struct{}
	added     chan struct{}
}

func newThreadWorkQueue() *threadWorkQueue {
	return &threadWorkQueue{
		inFlight:  make(map[string]struct{}),
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
		q.items = append(q.items[:index], q.items[index+1:]...)
		return work, true
	}
	return messageWork{}, false
}

func (q *threadWorkQueue) finish(threadID string) {
	q.mu.Lock()
	delete(q.inFlight, threadID)
	q.mu.Unlock()
}

type workResult struct {
	work messageWork
	err  error
}

func (a *App) dispatch(ctx context.Context, work *threadWorkQueue) error {
	jobs := make(chan messageWork)
	results := make(chan workResult, a.concurrency)
	var workers sync.WaitGroup
	workers.Add(a.concurrency)
	for range a.concurrency {
		go func() {
			defer workers.Done()
			for item := range jobs {
				results <- workResult{work: item, err: a.processWork(ctx, item)}
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
	for {
		for firstErr == nil && active < a.concurrency {
			item, ok := work.take()
			if !ok {
				break
			}
			jobs <- item
			active++
		}
		if active == 0 {
			break
		}

		select {
		case result := <-results:
			active--
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
		case <-work.added:
		}
	}

	close(stopClaims)
	claimLoop.Wait()
	claimErrMu.Lock()
	if claimErr != nil && firstErr == nil {
		firstErr = claimErr
	}
	claimErrMu.Unlock()

	close(jobs)
	workers.Wait()
	return firstErr
}
