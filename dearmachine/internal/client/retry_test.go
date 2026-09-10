package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"syscall"
	"testing"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"sendmux.ai/go/core"
)

func TestTransportRetryClassification(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 408, 429, 500, 502, 503, 504} {
		for _, err := range []error{&agentmail.Error{StatusCode: status}, &openMailAPIError{StatusCode: status}, &core.APIError{Status: status}} {
			want := status == 408 || status == 429 || status >= 500
			if got := transientTransportError(fmt.Errorf("wrapped: %w", err)); got != want {
				t.Errorf("%T status %d = %v", err, status, got)
			}
		}
	}
	for _, err := range []error{io.EOF, io.ErrUnexpectedEOF, syscall.ECONNRESET, syscall.ECONNREFUSED, syscall.EPIPE, context.DeadlineExceeded} {
		if !transientTransportError(err) {
			t.Errorf("not retryable: %v", err)
		}
	}
	for _, err := range []error{context.Canceled, errors.New("invalid config"), ErrAttachmentTooLarge} {
		if transientTransportError(err) {
			t.Errorf("retryable: %v", err)
		}
	}
}

func TestTransportRetryGrowthCapResetAndBudget(t *testing.T) {
	transport := WithTransportRetries(nil).(*retryTransport)
	var delays []time.Duration
	transport.wait = func(_ context.Context, delay time.Duration) error { delays = append(delays, delay); return nil }
	transport.jitter = func(cap time.Duration) time.Duration { return cap }
	calls := 0
	err := transport.retry(context.Background(), "fixture", func() error { calls++; return syscall.ECONNRESET })
	if !errors.Is(err, syscall.ECONNRESET) || calls != 7 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	if !reflect.DeepEqual(delays, want) {
		t.Fatalf("delays=%v", delays)
	}
	if retryDelay(1000) != 30*time.Second {
		t.Fatal("backoff exceeded cap")
	}
	delays = nil
	calls = 0
	for i := 0; i < 2; i++ {
		err := transport.retry(context.Background(), "fixture", func() error {
			calls++
			if calls%2 == 1 {
				return syscall.ECONNRESET
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(delays, []time.Duration{time.Second, time.Second}) {
		t.Fatalf("did not reset: %v", delays)
	}
}

func TestTransportRetryJitterAndCancellation(t *testing.T) {
	transport := WithTransportRetries(nil).(*retryTransport)
	for attempt := 0; attempt < 20; attempt++ {
		cap := retryDelay(attempt)
		for i := 0; i < 100; i++ {
			if got := transport.jitter(cap); got < cap/2 || got > cap {
				t.Fatalf("jitter %s outside [%s,%s]", got, cap/2, cap)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	transport.wait = func(ctx context.Context, delay time.Duration) error { cancel(); return waitTransportRetry(ctx, delay) }
	calls := 0
	err := transport.retry(ctx, "fixture", func() error { calls++; return syscall.ECONNRESET })
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	err = transport.retry(ctx, "fixture", func() error { t.Fatal("called after cancellation"); return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type retryMailFixture struct {
	Transport
	polls, sends, receipts int
}

func (f *retryMailFixture) Poll(context.Context) ([]Message, error) {
	f.polls++
	if f.polls%2 == 1 {
		return nil, syscall.ECONNRESET
	}
	return []Message{{MessageID: "inbound"}}, nil
}
func (f *retryMailFixture) Reply(_ context.Context, _ string, _ ReplyPayload, key string) (string, error) {
	f.sends++
	if key != "durable-key" {
		panic("idempotency key changed")
	}
	// Delivery succeeded but the response was lost.
	return "", syscall.ECONNRESET
}
func (f *retryMailFixture) ReplyReceipt(context.Context, Message) (string, bool, error) {
	f.receipts++
	if f.receipts == 1 {
		return "", false, syscall.ECONNRESET
	}
	return "outbound", true, nil
}
func TestTransportRetryLostReplyUsesReceiptWithoutDuplicateSend(t *testing.T) {
	raw := &retryMailFixture{}
	transport := WithTransportRetries(raw).(*retryTransport)
	transport.wait = func(context.Context, time.Duration) error { return nil }
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		messages, err := transport.Poll(ctx)
		if err != nil || len(messages) != 1 {
			t.Fatalf("poll=%v,%v", messages, err)
		}
	}
	if _, err := transport.Reply(ctx, "inbound", ReplyPayload{Text: "done"}, "durable-key"); !errors.Is(err, syscall.ECONNRESET) {
		t.Fatal(err)
	}
	id, found, err := transport.ReplyReceipt(ctx, Message{MessageID: "inbound"})
	if err != nil || !found || id != "outbound" || raw.sends != 1 || raw.receipts != 2 {
		t.Fatalf("receipt=%q,%v,%v sends=%d receipts=%d", id, found, err, raw.sends, raw.receipts)
	}
}

func TestTransportRetryPermanentErrorFailsImmediately(t *testing.T) {
	transport := WithTransportRetries(nil).(*retryTransport)
	transport.wait = func(context.Context, time.Duration) error { t.Fatal("wait for permanent failure"); return nil }
	err := transport.retry(context.Background(), "fixture", func() error { return &openMailAPIError{StatusCode: 401} })
	if err == nil {
		t.Fatal("lost authentication failure")
	}
}
