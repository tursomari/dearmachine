package client

import (
	"context"
	"errors"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"syscall"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"sendmux.ai/go/core"
)

// retryTransport retries only reads and idempotent acknowledgements. Reply stays
// a single attempt: an uncertain send must go through durable receipt recovery.
type retryTransport struct {
	Transport
	wait   func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

func WithTransportRetries(transport Transport) Transport {
	return &retryTransport{Transport: transport, wait: waitTransportRetry, jitter: func(cap time.Duration) time.Duration {
		return cap/2 + time.Duration(rand.Int64N(int64(cap-cap/2)+1))
	}}
}

func waitTransportRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func retryDelay(attempt int) time.Duration {
	delay := time.Second
	for i := 0; i < attempt && delay < 30*time.Second; i++ {
		delay *= 2
	}
	if delay > 30*time.Second {
		return 30 * time.Second
	}
	return delay
}

func transientTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var agentError *agentmail.Error
	var openError *openMailAPIError
	var sendError *core.APIError
	status := 0
	switch {
	case errors.As(err, &agentError):
		status = agentError.StatusCode
	case errors.As(err, &openError):
		status = openError.StatusCode
	case errors.As(err, &sendError):
		status = sendError.Status
	}
	if status != 0 {
		return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500 && status <= 599
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return true
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EPIPE)
}

func (transport *retryTransport) retry(ctx context.Context, operation string, call func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := call()
		if err == nil || !transientTransportError(err) || attempt == 6 {
			return err
		}
		delay := transport.jitter(retryDelay(attempt))
		// Provider diagnostics can contain sensitive response bodies; log only the
		// operation, attempt, and delay here.
		log.Printf("transport %s: transient failure; retry %d/6 in %s", operation, attempt+1, delay)
		if err := transport.wait(ctx, delay); err != nil {
			return err
		}
	}
}

func (transport *retryTransport) Poll(ctx context.Context) (messages []Message, err error) {
	err = transport.retry(ctx, "poll", func() error { messages, err = transport.Transport.Poll(ctx); return err })
	return
}
func (transport *retryTransport) Thread(ctx context.Context, id string) (messages []Message, err error) {
	err = transport.retry(ctx, "thread", func() error { messages, err = transport.Transport.Thread(ctx, id); return err })
	return
}
func (transport *retryTransport) Message(ctx context.Context, id string) (message Message, err error) {
	err = transport.retry(ctx, "message", func() error { message, err = transport.Transport.Message(ctx, id); return err })
	return
}
func (transport *retryTransport) ReplyReceipt(ctx context.Context, message Message, recipient string) (id string, found bool, err error) {
	err = transport.retry(ctx, "receipt", func() error { id, found, err = transport.Transport.ReplyReceipt(ctx, message, recipient); return err })
	return
}
func (transport *retryTransport) MarkProcessed(ctx context.Context, id string) error {
	return transport.retry(ctx, "acknowledge", func() error { return transport.Transport.MarkProcessed(ctx, id) })
}
func (transport *retryTransport) FetchAttachment(ctx context.Context, id string, maxBytes int64) (data []byte, err error) {
	err = transport.retry(ctx, "attachment", func() error { data, err = transport.Transport.FetchAttachment(ctx, id, maxBytes); return err })
	return
}

func (transport *retryTransport) AuthorizePair(ctx context.Context, email string) error {
	if authorizer, ok := transport.Transport.(PairAuthorizer); ok {
		return authorizer.AuthorizePair(ctx, email)
	}
	return nil
}
