package client

import (
	"context"
	"encoding/json"
	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
	"net/http"
	"net/http/httptest"
	"sendmux.ai/go/mailbox"
	"sendmux.ai/go/sending"
	"slices"
	"strings"
	"testing"
)

func TestAgentMailReplyExplicitCC(t *testing.T) {
	body := captureMailboxReplyBody(t, ReplyPayload{Text: "answer", To: []string{"owner@example.test"}, CC: []string{"guest@example.test"}})
	var cc []string
	if err := json.Unmarshal(body["cc"], &cc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cc, []string{"guest@example.test"}) {
		t.Fatalf("cc=%v", cc)
	}
}
func TestOpenMailReplyExplicitCCJSONAndAttachments(t *testing.T) {
	for _, files := range [][]OutboundFile{nil, {{Filename: "a.txt", Contents: []byte("answer")}}} {
		fake := newFakeOpenMailAPI(t)
		m := fake.transport(t, "inb-test", true)
		payload := ReplyPayload{Text: "answer", To: []string{"owner@example.test"}, CC: []string{"guest@example.test", "second@example.test"}, Files: files}
		req, err := m.replyRequest(context.Background(), "inb-test", payload.To[0], "thread", payload.Text, payload, "key")
		if err != nil {
			t.Fatal(err)
		}
		var cc []string
		if len(files) == 0 {
			var body struct {
				CC []string `json:"cc"`
			}
			if err = json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			cc = body.CC
		} else {
			if err = req.ParseMultipartForm(1024); err != nil {
				t.Fatal(err)
			}
			defer req.MultipartForm.RemoveAll()
			cc = req.MultipartForm.Value["cc"]
		}
		if !slices.Equal(cc, payload.CC) {
			t.Fatalf("files=%d cc=%v", len(files), cc)
		}
	}
}
func TestGuestAgentMailDirectionalOwnership(t *testing.T) {
	for _, direction := range []string{"receive", "reply", "send"} {
		present := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path != "/v0/inboxes/inbox/lists/"+direction+"/allow" && r.URL.Path != "/v0/inboxes/inbox/lists/"+direction+"/allow/guest@example.test" {
				t.Errorf("wrong direction path %s", r.URL.Path)
			}
			switch r.Method {
			case "GET":
				if !present {
					w.WriteHeader(404)
					return
				}
			case "POST":
				present = true
			case "DELETE":
				present = false
				w.WriteHeader(204)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"entry": "guest@example.test", "entry_type": "email", "direction": direction, "list_type": "allow", "inbox_id": "inbox", "created_at": "2026-09-12T00:00:00Z"})
		}))
		m, err := NewMailbox(agentmail.NewClient(option.WithBaseURL(srv.URL+"/"), option.WithAPIKey("offline"), option.WithMaxRetries(0)), "inbox")
		if err != nil {
			t.Fatal(err)
		}
		p := m.GuestPermissionAuthorizers()[direction]
		created, err := p.AddReceive(context.Background(), "guest@example.test")
		if err != nil {
			t.Fatal(err)
		}
		if err = p.RemoveReceive(context.Background(), "guest@example.test", created.Token); err != nil {
			t.Fatal(err)
		}
		if present {
			t.Fatal("owned directional rule remained")
		}
		srv.Close()
	}
}

func TestSendmuxReplyExplicitCCBothAPIs(t *testing.T) {
	fake := newFakeSendmuxAPI("")
	m, err := newSendmuxTransport(sendmuxTransportConfig{API: fake, Inbox: "mbx-test", AllowMutation: true})
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Reply(context.Background(), "message-new", ReplyPayload{Text: "answer", To: []string{"owner@example.test"}, CC: []string{"guest@example.test"}}, "key")
	if err != nil {
		t.Fatal(err)
	}
	request := fake.sends[0]
	if !slices.Equal(request.CC, []string{"guest@example.test"}) {
		t.Fatalf("send CC=%v", request.CC)
	}
	for _, kind := range []string{"mailbox", "sending"} {
		captured := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			captured = true
			w.Header().Set("Content-Type", "application/json")
			var body struct {
				CC []struct {
					Email string `json:"email"`
				} `json:"cc"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if len(body.CC) != 1 || body.CC[0].Email != "guest@example.test" {
				t.Errorf("%s body CC=%v", kind, body.CC)
			}
			w.Write([]byte(`{"ok":true,"meta":{"request_id":"offline-request"},"data":{"message_id":"eml_aaaaaaaaaaaaaaaaaaaaaaaa","status":"queued"}}`))
		}))
		if kind == "mailbox" {
			sdk, err := mailbox.New("smx_mbx_"+strings.Repeat("a", 32), mailbox.WithBaseURL(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = (&sendmuxSDKAPI{client: sdk}).Send(context.Background(), "mbx-test", request, "key")
			if err != nil {
				t.Fatal(err)
			}
		} else {
			sdk, err := sending.New("smx_mbx_"+strings.Repeat("a", 32), sending.WithBaseURL(srv.URL))
			if err != nil {
				t.Fatal(err)
			}
			_, err = (&sendmuxSDKSendingAPI{client: sdk}).Send(context.Background(), "machine@example.test", request, "key")
			if err != nil {
				t.Fatal(err)
			}
		}
		if !captured {
			t.Fatal("SDK did not submit reply")
		}
		srv.Close()
	}
}
