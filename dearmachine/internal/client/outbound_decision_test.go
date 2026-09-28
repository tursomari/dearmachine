package client

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Exercise the decision boundary directly, independently of recovery and agent
// dispatch. Preserve the explicitly supplied authentication evidence.
func outboundFeedbackMessage(f *outboundFixture, body string) Message {
	return Message{MessageID: "feedback-control", ThreadID: f.message.ThreadID, From: f.pair.UserEmail,
		To: []string{f.inbox.Address}, CC: []string{"guest@example.test"}, Body: body,
		InReplyTo: f.raw.sentReplies()[0].ReceiptID, authenticated: true,
		Delivery: MessageDelivery{InboxID: f.inbox.ProviderID, Recipient: f.inbox.Address}}
}
func rememberOutboundFeedback(f *outboundFixture, m Message) {
	f.raw.setThread(m.ThreadID, append(f.raw.thread(m.ThreadID), m))
	f.router.mu.Lock()
	f.router.remember(f.pair.ID, m)
	f.router.mu.Unlock()
}
func requireOutboundFeedback(t *testing.T, f *outboundFixture, m Message, want string) {
	t.Helper()
	replies := f.raw.sentReplies()
	if len(replies) != 2 {
		t.Fatalf("sends=%d, want preview and one notice", len(replies))
	}
	n := replies[1]
	if n.MessageID != m.MessageID || !reflect.DeepEqual(n.To, []string{f.pair.UserEmail}) || n.CC != nil || n.BCC != nil || len(n.Files) != 0 || n.IncludeQuotedContent {
		t.Fatalf("unsafe notice envelope: %+v", n)
	}
	if n.Text != want || strings.Contains(n.HTML, f.payload.Text) || strings.Contains(n.Text, f.payload.Text) || n.IdempotencyKey != outboundNoticeKey("control", m.MessageID) {
		t.Fatalf("unexpected notice: %+v", n)
	}
	seen, err := f.rig.store.Seen(m.MessageID)
	if err != nil || !seen {
		t.Fatalf("control not recorded: seen=%v err=%v", seen, err)
	}
}

func TestOutboundDecisionFeedbackReasonsAndReplay(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"invalid", "Not recognised — reply with only yes or no to the preview."},
		{"superseded", "That preview was replaced; reply to the newer preview."},
		{"ambiguous", "That reply matched more than one preview; reply directly to one."},
		{"unmatched", "To approve a pending reply, answer its private preview. This message was not sent to Machtiani."},
		{"unknown-token", "To approve a pending reply, answer its private preview. This message was not sent to Machtiani."},
		{"rejected", "That reply was already rejected; this decision did not send it."},
		{"sent", "That reply was already sent; this decision did not send it again."},
		{"approved", "That reply was already approved; this decision did not start another send."},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			f := newOutboundFixture(t, "guest@example.test")
			f.queue(t)
			m := outboundFeedbackMessage(f, "yes")
			o := f.records(t)[0]
			switch tc.mode {
			case "invalid":
				m.Body = "Yes, send it"
			case "unmatched":
				m.InReplyTo = f.message.MessageID
			case "unknown-token":
				m.InReplyTo = ""
				m.RawBody = "yes\n> " + outboundReferencePrefix + strings.Repeat("0", 32)
			case "ambiguous":
				second := o
				second.Key += "-second"
				second.Token = strings.Repeat("a", 32)
				second.PreviewID = "second-preview"
				if err := f.rig.store.saveOutbound(second); err != nil {
					t.Fatal(err)
				}
				m.RawBody = "yes\n> " + outboundReferencePrefix + o.Token + "\n> " + outboundReferencePrefix + second.Token
			default:
				o.State = tc.mode
				if err := f.rig.store.saveOutbound(o); err != nil {
					t.Fatal(err)
				}
			}
			before := f.records(t)
			rememberOutboundFeedback(f, m)
			handled, err := f.rig.app.handleOutboundDecision(context.Background(), m)
			if err != nil || !handled {
				t.Fatalf("handled=%v err=%v", handled, err)
			}
			requireOutboundFeedback(t, f, m, tc.want)
			if !reflect.DeepEqual(before, f.records(t)) {
				t.Fatal("invalid control changed approval evidence")
			}
			f.rig.restartStore(t)
			if _, err := f.rig.app.handleOutboundDecision(context.Background(), m); err != nil {
				t.Fatal(err)
			}
			requireOutboundFeedback(t, f, m, tc.want)
		})
	}
}

func TestOutboundDecisionFeedbackScopeAndLoopGuards(t *testing.T) {
	for _, mode := range []string{"guest", "unauthenticated", "thread", "empty-thread", "pair", "inbox", "owner", "unknown-scope", "sent-label", "machine-sender", "ordinary", "bare-without-reference", "instruction"} {
		t.Run(mode, func(t *testing.T) {
			f := newOutboundFixture(t, "guest@example.test")
			f.queue(t)
			m := outboundFeedbackMessage(f, "yes please")
			o := f.records(t)[0]
			switch mode {
			case "guest":
				m.From = "guest@example.test"
			case "unauthenticated":
				m.authenticated = false
			case "thread":
				m.ThreadID = "another-thread"
			case "empty-thread":
				m.ThreadID = ""
			case "pair":
				o.PairID = "another-pair"
			case "inbox":
				o.InboxID = "another-inbox"
			case "owner":
				o.Owner = "previous-owner@example.test"
			case "unknown-scope":
				m.ThreadID = "unknown-thread"
				m.InReplyTo = ""
				m.RawBody = outboundReferencePrefix + strings.Repeat("0", 32)
			case "sent-label":
				m.Labels = []string{"sent"}
			case "machine-sender":
				m.From = f.inbox.Address
			case "ordinary":
				m.InReplyTo = f.message.MessageID
			case "bare-without-reference":
				m.InReplyTo = ""
				m.Body = "yes"
			case "instruction":
				request, _, err := f.rig.store.BeginParticipantRequest(Message{MessageID: "instruction", ThreadID: m.ThreadID, From: "guest@example.test"}, f.pair.UserEmail)
				if err != nil {
					t.Fatal(err)
				}
				if err := f.rig.store.SetParticipantPrompt(request.RequestMessageID, participantAwaitingDecision, "instruction-preview"); err != nil {
					t.Fatal(err)
				}
				m.InReplyTo = "instruction-preview"
				m.RawBody = "yes\n> " + outboundReferencePrefix + o.Token
			}
			if err := f.rig.store.saveOutbound(o); err != nil {
				t.Fatal(err)
			}
			rememberOutboundFeedback(f, m)
			handled, err := f.rig.app.handleOutboundDecision(context.Background(), m)
			if err != nil && !errors.Is(err, ErrGuestUnauthorized) {
				t.Fatal(err)
			}
			if len(f.raw.sentReplies()) != 1 || f.records(t)[0].Decision != "" {
				t.Fatal("out-of-scope control sent feedback or decided")
			}
			if (mode == "ordinary" || mode == "bare-without-reference" || mode == "instruction" || mode == "sent-label" || mode == "machine-sender") && handled {
				t.Fatal("broadened control detection")
			}
		})
	}
}

type outboundFeedbackUnavailableThread struct {
	*fakeTransport
	calls int
}

func (f *outboundFeedbackUnavailableThread) Thread(context.Context, string) ([]Message, error) {
	f.calls++
	return nil, errors.New("preview reconciliation provider unavailable")
}

func TestOutboundDecisionFeedbackPreviewSendingDefers(t *testing.T) {
	for _, matched := range []bool{false, true} {
		t.Run(map[bool]string{false: "unmatched-reference", true: "token"}[matched], func(t *testing.T) {
			f := newOutboundFixture(t, "guest@example.test")
			f.queue(t)
			o := f.records(t)[0]
			o.State = "preview_sending"
			o.PreviewID = ""
			raw := &outboundFeedbackUnavailableThread{fakeTransport: f.raw}
			f.router.raw = raw
			if err := f.rig.store.saveOutbound(o); err != nil {
				t.Fatal(err)
			}
			m := outboundFeedbackMessage(f, "yes")
			if matched {
				m.RawBody = "yes\n> " + outboundReferencePrefix + o.Token
			}
			rememberOutboundFeedback(f, m)
			handled, err := f.rig.app.handleOutboundDecision(context.Background(), m)
			seen, seenErr := f.rig.store.Seen(m.MessageID)
			if err != nil || !handled || seenErr != nil || seen || len(f.raw.sentReplies()) != 1 || raw.calls != 0 {
				t.Fatalf("decision not deferred: handled=%v seen=%v err=%v/%v", handled, seen, err, seenErr)
			}
		})
	}
}

func TestOutboundDecisionFeedbackRecordedBeforeControl(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	m := outboundFeedbackMessage(f, "yes please")
	rememberOutboundFeedback(f, m)
	_, err := f.rig.store.db.Exec(`CREATE TRIGGER fail_control BEFORE INSERT ON processed_messages BEGIN SELECT RAISE(FAIL, 'control recording failed'); END`)
	if err != nil {
		t.Fatal(err)
	}
	handled, err := f.rig.app.handleOutboundDecision(context.Background(), m)
	if !handled || err == nil {
		t.Fatalf("expected control DB failure, handled=%v err=%v", handled, err)
	}
	seen, err := f.rig.store.Seen(m.MessageID)
	if err != nil || seen || len(f.raw.sentReplies()) != 2 {
		t.Fatalf("notice must precede control recording: seen=%v err=%v", seen, err)
	}
	if _, err := f.rig.store.db.Exec(`DROP TRIGGER fail_control`); err != nil {
		t.Fatal(err)
	}
	f.rig.restartStore(t)
	if _, err := f.rig.app.handleOutboundDecision(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	requireOutboundFeedback(t, f, m, "Not recognised — reply with only yes or no to the preview.")
}

type outboundFeedbackFailingTransport struct {
	*fakeTransport
	attempts int
}

func (f *outboundFeedbackFailingTransport) Reply(ctx context.Context, id string, p ReplyPayload, key string) (string, error) {
	if strings.HasPrefix(key, "dearmachine-outbound-control-") {
		f.attempts++
		return "", errors.New("notice provider unavailable")
	}
	return f.fakeTransport.Reply(ctx, id, p, key)
}
func TestOutboundDecisionFeedbackProviderFailureConsumedOnce(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	raw := &outboundFeedbackFailingTransport{fakeTransport: f.raw}
	f.router.raw = raw
	m := outboundFeedbackMessage(f, "yes please")
	rememberOutboundFeedback(f, m)
	for i := 0; i < 2; i++ {
		handled, err := f.rig.app.handleOutboundDecision(context.Background(), m)
		if !handled || err != nil {
			t.Fatalf("provider failure blocked processing: handled=%v err=%v", handled, err)
		}
		seen, err := f.rig.store.Seen(m.MessageID)
		if err != nil || !seen {
			t.Fatalf("failed notice not consumed: seen=%v err=%v", seen, err)
		}
		f.rig.restartStore(t)
	}
	if raw.attempts != 1 || len(f.raw.sentReplies()) != 1 {
		t.Fatal("failed notice attempted twice")
	}
}

func TestOutboundDecisionFeedbackNoticeDBFailureLeavesControlUnseen(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	m := outboundFeedbackMessage(f, "yes please")
	rememberOutboundFeedback(f, m)
	if _, err := f.rig.store.db.Exec(`CREATE TRIGGER fail_notice BEFORE INSERT ON outbound_notices BEGIN SELECT RAISE(FAIL, 'notice recording failed'); END`); err != nil {
		t.Fatal(err)
	}
	handled, err := f.rig.app.handleOutboundDecision(context.Background(), m)
	if !handled || err == nil {
		t.Fatalf("expected notice DB failure: handled=%v err=%v", handled, err)
	}
	seen, err := f.rig.store.Seen(m.MessageID)
	if err != nil || seen || len(f.raw.sentReplies()) != 1 {
		t.Fatalf("failed durable notice consumed or sent: seen=%v err=%v", seen, err)
	}
	if _, err := f.rig.store.db.Exec(`DROP TRIGGER fail_notice`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.rig.app.handleOutboundDecision(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	requireOutboundFeedback(t, f, m, "Not recognised — reply with only yes or no to the preview.")
}

func TestOutboundDecisionFeedbackSuccessfulDecisionRetryHasNoNotice(t *testing.T) {
	f := newOutboundFixture(t, "guest@example.test")
	f.queue(t)
	m := outboundFeedbackMessage(f, "no")
	rememberOutboundFeedback(f, m)
	if _, err := f.rig.store.db.Exec(`CREATE TRIGGER fail_control BEFORE INSERT ON processed_messages BEGIN SELECT RAISE(FAIL, 'control recording failed'); END`); err != nil {
		t.Fatal(err)
	}
	if handled, err := f.rig.app.handleOutboundDecision(context.Background(), m); !handled || err == nil {
		t.Fatalf("expected recording failure: handled=%v err=%v", handled, err)
	}
	if o := f.records(t)[0]; o.State != "rejected" || o.DecisionID != m.MessageID {
		t.Fatal("decision was not persisted")
	}
	if _, err := f.rig.store.db.Exec(`DROP TRIGGER fail_control`); err != nil {
		t.Fatal(err)
	}
	f.rig.restartStore(t)
	if _, err := f.rig.app.handleOutboundDecision(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	if len(f.raw.sentReplies()) != 1 {
		t.Fatal("successful decision retry produced feedback")
	}
	seen, err := f.rig.store.Seen(m.MessageID)
	if err != nil || !seen {
		t.Fatalf("retry not recorded: seen=%v err=%v", seen, err)
	}
}
