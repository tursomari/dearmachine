package client

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
	"sendmux.ai/go/core"
	"sendmux.ai/go/mailbox"
	"sendmux.ai/go/sending"
)

type replyBoundaryRoundTripper func(*http.Request) (*http.Response, error)

func (f replyBoundaryRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func assertReplySubmissionBoundary(t *testing.T, err error, wantBefore bool) {
	t.Helper()
	var before *replyNotSubmitted
	if err == nil || errors.As(err, &before) != wantBefore {
		t.Fatalf("error = %v, want before submission = %v", err, wantBefore)
	}
}

func TestAgentMailReplySubmissionBoundary(t *testing.T) {
	for _, mode := range []string{"cancel-before", "503", "400", "lost-response", "cancel-during", "missing-receipt"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			client := &http.Client{Transport: replyBoundaryRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "boundary-key" {
					t.Fatalf("unexpected reply request: %s, key %q", r.Method, r.Header.Get("Idempotency-Key"))
				}
				if mode == "lost-response" {
					return nil, io.ErrUnexpectedEOF
				}
				if mode == "cancel-during" {
					cancel()
					return nil, context.Canceled
				}
				status, body := http.StatusServiceUnavailable, `{"message":"unavailable"}`
				if mode == "400" {
					status = http.StatusBadRequest
				}
				if mode == "missing-receipt" {
					status, body = http.StatusOK, `{}`
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			m, err := NewMailbox(agentmail.NewClient(option.WithAPIKey("offline-key"), option.WithBaseURL("https://provider.invalid"), option.WithHTTPClient(client), option.WithMaxRetries(2)), "inbox@example.test")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cancel-before" {
				cancel()
			}
			_, err = m.Reply(ctx, "parent", ReplyPayload{Text: "answer"}, "boundary-key")
			assertReplySubmissionBoundary(t, err, mode == "cancel-before")
			wantCalls := 1
			if mode == "cancel-before" {
				wantCalls = 0
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation not preserved: %v", err)
				}
			}
			if calls != wantCalls {
				t.Fatalf("HTTP attempts = %d, want %d (SDK retries must be disabled per reply)", calls, wantCalls)
			}
		})
	}
}

func TestOpenMailReplySubmissionBoundary(t *testing.T) {
	for _, mode := range []string{"cancel-before", "empty-key", "lookup-error", "empty-body", "bcc", "cancel-after-lookup", "send-error", "http-400", "http-503", "missing-receipt"} {
		t.Run(mode, func(t *testing.T) {
			fake := newFakeOpenMailAPI(t)
			transport := fake.transport(t, "device@openmail.sh", true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := transport.httpClient.Transport
			calls, writes := 0, 0
			lookupErr := errors.New("read-only lookup failed")
			transport.httpClient = &http.Client{Transport: replyBoundaryRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method == http.MethodPost {
					writes++
					if mode == "send-error" {
						return nil, io.ErrUnexpectedEOF
					}
					status := http.StatusOK
					if mode == "http-400" {
						status = http.StatusBadRequest
					}
					if mode == "http-503" {
						status = http.StatusServiceUnavailable
					}
					return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: r}, nil
				}
				if mode == "lookup-error" {
					return nil, lookupErr
				}
				response, err := base.RoundTrip(r)
				if mode == "cancel-after-lookup" && strings.HasSuffix(r.URL.Path, "/messages") {
					cancel()
				}
				return response, err
			})}
			payload, key := ReplyPayload{Text: "answer"}, "boundary-key"
			switch mode {
			case "cancel-before":
				cancel()
			case "empty-key":
				key = ""
			case "empty-body":
				payload.Text = ""
			case "bcc":
				payload.BCC = []string{"hidden@example.test"}
			}
			_, err := transport.Reply(ctx, "message-new", payload, key)
			before := mode != "send-error" && mode != "missing-receipt" && !strings.HasPrefix(mode, "http-")
			assertReplySubmissionBoundary(t, err, before)
			if mode == "lookup-error" && !errors.Is(err, lookupErr) {
				t.Fatalf("lookup cause not preserved: %v", err)
			}
			if before && writes != 0 || !before && writes != 1 {
				t.Fatalf("mutating calls = %d, before = %v", writes, before)
			}
			if mode == "cancel-before" && calls != 0 {
				t.Fatalf("canceled reply made %d requests", calls)
			}
		})
	}
}

type replyBoundarySendmuxAPI struct {
	*fakeSendmuxAPI
	mode   string
	cancel context.CancelFunc
	writes int
}

func (f *replyBoundarySendmuxAPI) ResolveMailbox(ctx context.Context, inbox string) (sendmuxMailboxInfo, error) {
	if f.mode == "lookup-error" {
		return sendmuxMailboxInfo{}, io.ErrUnexpectedEOF
	}
	return f.fakeSendmuxAPI.ResolveMailbox(ctx, inbox)
}

func (f *replyBoundarySendmuxAPI) Message(ctx context.Context, inbox, id string) (sendmuxRawMessage, error) {
	if f.mode == "message-error" {
		return sendmuxRawMessage{}, io.ErrUnexpectedEOF
	}
	m, err := f.fakeSendmuxAPI.Message(ctx, inbox, id)
	if f.mode == "cancel-after-lookup" {
		f.cancel()
	}
	return m, err
}

func (f *replyBoundarySendmuxAPI) Send(context.Context, string, sendmuxSendRequest, string) (string, error) {
	f.writes++
	if strings.HasSuffix(f.mode, "missing-receipt") {
		return "", nil
	}
	return "", io.ErrUnexpectedEOF
}

func TestSendmuxReplySubmissionBoundary(t *testing.T) {
	for _, mode := range []string{"cancel-before", "empty-key", "long-key", "lookup-error", "message-error", "empty-body", "cancel-after-lookup", "send-error", "missing-receipt", "outbound-error", "outbound-missing-receipt"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := &replyBoundarySendmuxAPI{fakeSendmuxAPI: newFakeSendmuxAPI(""), mode: mode, cancel: cancel}
			transport, err := newSendmuxTransport(sendmuxTransportConfig{API: fake, Inbox: "device@myagent.mx", AllowMutation: true})
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "outbound-") {
				transport.outbound = fake
			}
			payload, key := ReplyPayload{Text: "answer"}, "boundary-key"
			switch mode {
			case "cancel-before":
				cancel()
			case "empty-key":
				key = ""
			case "long-key":
				key = strings.Repeat("k", 256)
			case "empty-body":
				payload.Text = ""
			}
			_, err = transport.Reply(ctx, "message-new", payload, key)
			before := mode != "send-error" && mode != "missing-receipt" && !strings.HasPrefix(mode, "outbound-")
			assertReplySubmissionBoundary(t, err, before)
			if before && fake.writes != 0 || !before && fake.writes != 1 {
				t.Fatalf("mutating calls = %d, before = %v", fake.writes, before)
			}
		})
	}
}

func TestSendmuxSendingRecipientValidationBeforeSubmission(t *testing.T) {
	// No SDK client is needed: this validation must precede dispatch.
	api := &sendmuxSDKSendingAPI{}
	_, err := api.Send(context.Background(), "sender@example.test", sendmuxSendRequest{}, "key")
	assertReplySubmissionBoundary(t, err, true)
}

// Every case uses a real TCP connection that already completed a GET. The
// server consumes the first POST, then drops its response. A replayable control
// demonstrates that the fixture actually exercises net/http's hidden retry.
func TestReplySubmissionDoesNotReplayLostResponse(t *testing.T) {
	for _, provider := range []string{"replayable-control", "agentmail", "openmail-json", "openmail-multipart", "sendmux-mailbox", "sendmux-sending", "sendmux-jmap"} {
		t.Run(provider, func(t *testing.T) {
			var posts atomic.Int32
			var reused atomic.Bool
			var warmed sync.Map
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					warmed.Store(r.RemoteAddr, true)
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"data":[{"id":"parent","threadId":"thread","direction":"inbound","fromAddr":"owner@example.test"}],"total":1}`)
					return
				}
				body, err := io.ReadAll(r.Body)
				if err != nil || len(body) == 0 {
					t.Errorf("send body was not fully received: length=%d, err=%v", len(body), err)
				}
				if provider != "sendmux-jmap" && r.Header.Get("Idempotency-Key") != "boundary-key" {
					t.Error("send lost its idempotency key")
				}
				if posts.Add(1) == 1 {
					_, wasWarm := warmed.Load(r.RemoteAddr)
					reused.Store(wasWarm)
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Errorf("hijack: %v", err)
						return
					}
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{}`)
			}))
			defer server.Close()
			base := &http.Transport{MaxIdleConnsPerHost: 1}
			defer base.CloseIdleConnections()
			client := &http.Client{Transport: base, Timeout: 5 * time.Second}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			warm, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/warm", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(warm)
			if err != nil {
				t.Fatal(err)
			}
			_, readErr := io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if readErr != nil {
				t.Fatal(readErr)
			}
			err = replyBoundaryTCPSend(t, ctx, provider, server.URL, client)
			if !reused.Load() {
				t.Fatal("first POST did not use the warmed TCP connection")
			}
			wantPosts := int32(1)
			if provider == "replayable-control" {
				wantPosts = 2
				if err != nil {
					t.Fatalf("control did not replay successfully: %v", err)
				}
			} else {
				assertReplySubmissionBoundary(t, err, false)
			}
			if got := posts.Load(); got != wantPosts {
				t.Fatalf("accepted POSTs = %d, want %d", got, wantPosts)
			}
		})
	}
}

func replyBoundaryTCPSend(t *testing.T, ctx context.Context, provider, baseURL string, client *http.Client) error {
	t.Helper()
	switch provider {
	case "replayable-control":
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/send", bytes.NewReader([]byte(`{"body":"answer"}`)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Idempotency-Key", "boundary-key")
		response, err := client.Do(req)
		if response != nil {
			_ = response.Body.Close()
		}
		return err
	case "agentmail":
		m, err := NewMailbox(agentmail.NewClient(option.WithAPIKey("offline-key"), option.WithBaseURL(baseURL), option.WithHTTPClient(client)), "inbox@example.test")
		if err != nil {
			t.Fatal(err)
		}
		_, err = m.Reply(ctx, "parent", ReplyPayload{Text: "answer"}, "boundary-key")
		return err
	case "openmail-json", "openmail-multipart":
		transport, err := newOpenMailTransport(openMailTransportConfig{BaseURL: baseURL, APIKey: "offline-key", Inbox: "inbox", HTTPClient: client, AllowMutation: true})
		if err != nil {
			t.Fatal(err)
		}
		transport.resolvedInboxID = "inbox"
		payload := ReplyPayload{Text: "answer"}
		if provider == "openmail-multipart" {
			payload.Files = []OutboundFile{{Filename: "answer.txt", ContentType: "text/plain", Contents: []byte("answer")}}
		}
		_, err = transport.Reply(ctx, "parent", payload, "boundary-key")
		return err
	case "sendmux-mailbox":
		sdk, err := mailbox.New("smx_mbx_offline", mailbox.WithBaseURL(baseURL), mailbox.WithRetryOptions(core.RetryOptions{MaxAttempts: 1}), mailbox.WithHTTPClient(sendmuxNoReplayHTTPClient(client)))
		if err != nil {
			t.Fatal(err)
		}
		api := &sendmuxSDKAPI{client: sdk}
		_, err = api.Send(ctx, "inbox", sendmuxSendRequest{To: []string{"owner@example.test"}, Subject: "answer", Text: "answer"}, "boundary-key")
		return err
	case "sendmux-sending":
		sdk, err := sending.New("smx_mbx_offline", sending.WithBaseURL(baseURL), sending.WithRetryOptions(core.RetryOptions{MaxAttempts: 1}), sending.WithHTTPClient(sendmuxNoReplayHTTPClient(client)))
		if err != nil {
			t.Fatal(err)
		}
		api := &sendmuxSDKSendingAPI{client: sdk}
		_, err = api.Send(ctx, "sender@example.test", sendmuxSendRequest{To: []string{"owner@example.test"}, Subject: "answer", HTML: "<p>answer</p>"}, "boundary-key")
		return err
	case "sendmux-jmap":
		sender := &sendmuxJMAPSender{credential: "offline-key", origin: baseURL, client: client}
		var result map[string]any
		return sender.request(ctx, "sender@example.test", baseURL+"/jmap", "application/json", []byte(`{"methodCalls":[["EmailSubmission/set",{},"send"]]}`), &result)
	default:
		t.Fatalf("unknown provider %s", provider)
		return nil
	}
}

func TestReplySubmissionDoesNotReplayRedirect(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			for _, provider := range []string{"agentmail", "openmail-json", "openmail-multipart", "sendmux-mailbox", "sendmux-sending", "sendmux-jmap"} {
				t.Run(provider, func(t *testing.T) {
					var posts atomic.Int32
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/json")
						if r.Method == http.MethodGet {
							_, _ = io.WriteString(w, `{"data":[{"id":"parent","threadId":"thread","direction":"inbound","fromAddr":"owner@example.test"}],"total":1}`)
							return
						}
						_, _ = io.Copy(io.Discard, r.Body)
						if posts.Add(1) == 1 {
							w.Header().Set("Location", "/redirected-send")
							w.WriteHeader(status)
						}
						_, _ = io.WriteString(w, `{}`)
					}))
					defer server.Close()
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					err := replyBoundaryTCPSend(t, ctx, provider, server.URL, server.Client())
					assertReplySubmissionBoundary(t, err, false)
					if got := posts.Load(); got != 1 {
						t.Fatalf("redirect submitted %d POSTs, want 1", got)
					}
				})
			}
		})
	}
}

func TestSendmuxJMAPMutationCannotRewindBody(t *testing.T) {
	// Unlike HTTP/1, HTTP/2 can retry POSTs without an idempotency header.
	// Verify the request it receives cannot recreate a consumed body.
	calls := 0
	client := &http.Client{Transport: replyBoundaryRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Method != http.MethodPost || r.GetBody != nil || r.Body == nil || r.Body == http.NoBody {
			t.Fatal("JMAP mutation retained replay capability or lost its body")
		}
		return nil, io.ErrUnexpectedEOF
	})}
	sender := &sendmuxJMAPSender{credential: "offline-key", origin: "https://provider.invalid", client: client}
	var result map[string]any
	err := sender.request(context.Background(), "sender@example.test", "https://provider.invalid/jmap", "application/json", []byte(`{"methodCalls":[]}`), &result)
	assertReplySubmissionBoundary(t, err, false)
	if calls != 1 {
		t.Fatalf("JMAP requests = %d, want 1", calls)
	}
}
