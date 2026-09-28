package client

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/dearmachine/dearmachine/internal/synctrigger"
)

// Exercise the production polling loops, rather than manually invoking
// recoverOutbound after each decision. The busy lane remains blocked until the
// approved reply has been submitted (or the test fails).
func TestOutboundApprovalProgressWhileExecutionBusy(t *testing.T) {
	for _, busy := range []string{"worker", "maintenance"} {
		t.Run(busy, func(t *testing.T) {
			f := newOutboundFixture(t, "guest@example.test")
			f.queue(t)
			preview := f.raw.sentReplies()[0]
			app := f.rig.app
			app.pollInterval = 5 * time.Millisecond
			app.concurrency = 1
			started := make(chan struct{}, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			if busy == "worker" {
				runs := make(chan gatedRun, 4)
				invoke := gatedRunInvoker(runs, release)
				app.runner.invoke = func(cmd *exec.Cmd) error {
					if len(cmd.Args) > 1 && cmd.Args[1] == "sync" {
						return nil
					}
					if len(cmd.Args) > 1 && cmd.Args[1] == "run" {
						select {
						case started <- struct{}{}:
						default:
						}
					}
					return invoke(cmd)
				}
				m := Message{MessageID: "busy-request", ThreadID: "busy-thread",
					From: f.pair.UserEmail, To: []string{f.inbox.Address},
					Body: "Independent long-running work", Timestamp: time.Now().UTC()}
				f.raw.setThread(m.ThreadID, []Message{m})
				f.raw.setPoll([]Message{m})
				go func() { done <- app.ProcessOnce(ctx) }()
			} else {
				now := time.Now().UTC()
				app.syncOrchestrator = &synctrigger.Orchestrator{
					RepoPath: t.TempDir(), AgentBinary: "machtiani",
					PromptTemplatePath: filepath.Join(t.TempDir(), "prompt.md"),
					StatePath:          filepath.Join(t.TempDir(), "sync.json"),
					Lister: func(context.Context, string) ([]synctrigger.SessionInfo, error) {
						return []synctrigger.SessionInfo{{SessionID: "older", UpdatedAt: now},
							{SessionID: "newer", UpdatedAt: now.Add(time.Hour)}}, nil
					},
					GitLastCommitTime: func(string) (time.Time, error) { return time.Time{}, nil },
					RunCommand: func(ctx context.Context, _, _ string, _ ...string) ([]byte, error) {
						select {
						case started <- struct{}{}:
						default:
						}
						select {
						case <-release:
							return nil, nil
						case <-ctx.Done():
							return nil, ctx.Err()
						}
					},
				}
				go func() { done <- app.orchestrateWhilePolling(ctx) }()
			}
			t.Cleanup(func() {
				cancel()
				unblock()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("busy polling loop did not stop")
				}
			})
			select {
			case <-started:
			case err := <-done:
				done <- err
				t.Fatalf("execution exited before starting: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("independent execution did not start")
			}
			yes := Message{MessageID: "approve-during-work", ThreadID: f.message.ThreadID,
				From: f.pair.UserEmail, To: []string{f.inbox.Address}, Body: "yes",
				InReplyTo: preview.ReceiptID, Timestamp: time.Now().UTC()}
			f.raw.setThread(yes.ThreadID, append(f.raw.thread(yes.ThreadID), yes))
			f.raw.setPoll([]Message{yes})
			awaitOutboundState(t, f, "sent")
			replies := f.raw.sentReplies()
			if len(replies) != 2 || replies[1].MessageID != f.message.MessageID {
				t.Fatalf("approved reply did not submit while %s busy: replies=%d", busy, len(replies))
			}
			answer := replies[1]
			if answer.Text != f.payload.Text || answer.HTML != f.payload.HTML ||
				!reflect.DeepEqual(answer.Files, f.payload.Files) ||
				!sameRecipientSet(answer.To, f.payload.To) || !sameRecipientSet(answer.CC, f.payload.CC) {
				t.Fatal("busy polling changed the approved content or recipients")
			}
			select {
			case <-release:
				t.Fatal("busy work finished before submission")
			default:
			}
			// Poll the exact same approval again. It must not execute agent work
			// or resubmit the result; wait through several poll intervals.
			f.raw.setPoll([]Message{yes})
			time.Sleep(30 * time.Millisecond)
			if len(f.raw.sentReplies()) != 2 || f.records(t)[0].SubmissionAttempt.Count != 1 {
				t.Fatal("approval replay duplicated submission while busy")
			}
		})
	}
}

func awaitOutboundState(t *testing.T, f *outboundFixture, state string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if records := f.records(t); len(records) != 0 && records[len(records)-1].State == state {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("outbound state never reached %s while execution was blocked", state)
}

func TestOutboundPollAppliesWholeControlBatchBeforeSubmission(t *testing.T) {
	f := newOutboundFixture(t, "first@example.test", "second@example.test")
	f.queue(t)
	before := f.records(t)[0]
	token, err := f.router.guests.RemovalToken(GuestKey{
		f.pair.ID, guestInboxKey(f.inbox), "first@example.test", f.message.ThreadID,
	})
	if err != nil {
		t.Fatal(err)
	}
	yes := Message{MessageID: "batch-yes", ThreadID: f.message.ThreadID,
		From: f.pair.UserEmail, To: []string{f.inbox.Address}, Body: "yes",
		InReplyTo: before.PreviewID, Timestamp: time.Now().UTC()}
	remove := yes
	remove.MessageID, remove.Body = "batch-remove", "REMOVE GUEST "+token
	remove.Timestamp = yes.Timestamp.Add(time.Second)
	f.raw.setThread(yes.ThreadID, append(f.raw.thread(yes.ThreadID), yes, remove))
	f.raw.setPoll([]Message{yes, remove})
	if err := f.rig.app.pollAndClaim(context.Background(), newThreadWorkQueue()); err != nil {
		t.Fatal(err)
	}
	records := f.records(t)
	if len(records) != 2 || records[0].State != "superseded" || records[1].State != "pending" ||
		records[1].Revision != before.Revision+1 ||
		!sameRecipientSet(records[1].Payload.CC, []string{"second@example.test"}) {
		t.Fatal("approval was submitted before the same poll's removal required a fresh preview")
	}
	for _, reply := range f.raw.sentReplies() {
		if len(reply.CC) != 0 || !sameRecipientSet(reply.To, []string{f.pair.UserEmail}) {
			t.Fatal("same-batch revocation leaked a reply to guests")
		}
	}
}
