package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Stateful JMAP fake exercises response-loss recovery, not just request encoding.
type jmapFixture struct {
	delivered, undo string
	mu              sync.Mutex
	url             string
	email           map[string]any
	submissions     int
	state, substate int
	lose            string
	conflict        bool
	foreign         bool
}

func (f *jmapFixture) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	user, pass, ok := r.BasicAuth()
	if !ok || user != "machine@example.test" || pass != "test-mailbox-credential" {
		http.Error(w, "unauthorized", 401)
		return
	}
	reply := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/jmap/session" {
		api := f.url + "/jmap"
		if f.foreign {
			api = "https://foreign.example.test/jmap"
		}
		reply(map[string]any{"username": user, "apiUrl": api, "uploadUrl": f.url + "/upload/{accountId}", "primaryAccounts": map[string]string{"urn:ietf:params:jmap:mail": "account"}, "accounts": map[string]any{"account": map[string]any{}}, "capabilities": map[string]any{"urn:ietf:params:jmap:submission": map[string]any{}}})
		return
	}
	if r.URL.Path == "/upload/account" {
		reply(map[string]any{"accountId": "account", "blobId": "blob", "size": 3})
		return
	}
	var request struct{ MethodCalls [][]json.RawMessage }
	if json.NewDecoder(r.Body).Decode(&request) != nil || len(request.MethodCalls) != 1 {
		http.Error(w, "invalid", 400)
		return
	}
	var method, tag string
	var args map[string]any
	_ = json.Unmarshal(request.MethodCalls[0][0], &method)
	_ = json.Unmarshal(request.MethodCalls[0][1], &args)
	_ = json.Unmarshal(request.MethodCalls[0][2], &tag)
	result := map[string]any{"accountId": "account"}
	original := method
	switch method {
	case "Identity/get":
		result["list"] = []any{map[string]string{"id": "identity", "email": user}}
	case "Mailbox/get":
		result["list"] = []any{map[string]string{"id": "drafts", "role": "drafts"}, map[string]string{"id": "sent", "role": "sent"}}
	case "Email/get":
		result["state"] = fmt.Sprint(f.state)
		result["list"] = []any{}
		if len(args["ids"].([]any)) > 0 && f.email != nil {
			result["list"] = []any{f.email}
		}
	case "Email/query":
		result["ids"] = []string{}
		if f.email != nil {
			key := args["filter"].(map[string]any)["hasKeyword"].(string)
			if f.email["keywords"].(map[string]any)[key] == true {
				result["ids"] = []string{"email"}
			}
		}
	case "Email/set":
		if args["create"] != nil {
			if f.conflict {
				f.state++
				f.conflict = false
			}
			if args["ifInState"] != fmt.Sprint(f.state) {
				method = "error"
				result = map[string]any{"type": "stateMismatch"}
				break
			}
			if f.email != nil {
				panic("duplicate email creation")
			}
			f.email = args["create"].(map[string]any)["reply"].(map[string]any)
			f.email["id"] = "email"
			f.state++
			result["created"] = map[string]any{"reply": map[string]string{"id": "email"}}
		} else {
			patch := args["update"].(map[string]any)["email"].(map[string]any)
			f.email["mailboxIds"] = patch["mailboxIds"]
			f.email["keywords"] = patch["keywords"]
			f.state++
			result["updated"] = map[string]any{"email": nil}
		}
	case "EmailSubmission/get":
		result["state"] = fmt.Sprint(f.substate)
		if len(args["ids"].([]any)) > 0 {
			result["list"] = []any{map[string]any{"id": "submission", "emailId": "email", "undoStatus": f.undo, "deliveryStatus": map[string]any{"owner@example.test": map[string]string{"delivered": f.delivered, "smtpReply": "250 2.1.5 Queued"}}}}
		}
	case "EmailSubmission/query":
		result["ids"] = []string{}
		if f.submissions > 0 {
			result["ids"] = []string{"submission"}
		}
	case "EmailSubmission/set":
		if args["ifInState"] != fmt.Sprint(f.substate) {
			method = "error"
			result = map[string]any{"type": "stateMismatch"}
			break
		}
		f.submissions++
		f.substate++
		result["created"] = map[string]any{"send": map[string]string{"id": "submission"}}
	default:
		panic("unexpected method " + method)
	}
	if f.lose == original && method != "error" {
		f.lose = ""
		http.Error(w, "response lost", 502)
		return
	}
	reply(map[string]any{"methodResponses": []any{[]any{method, result, tag}}})
}
func newJMAPFixture(t *testing.T) (*sendmuxJMAPSender, *jmapFixture) {
	t.Helper()
	f := &jmapFixture{}
	server := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(server.Close)
	f.url = server.URL
	s := newSendmuxJMAPSender("test-mailbox-credential")
	s.origin = server.URL
	s.client = server.Client()
	return s, f
}
func jmapTestReply() sendmuxSendRequest {
	return sendmuxSendRequest{ParentRFCMessageID: "<parent@example.test>", References: []string{"<root@example.test>", "<parent@example.test>"}, To: []string{"owner@example.test"}, Subject: "Re: example", Text: "Approve this guest request?", HTML: "<p>Approve?</p>"}
}
func TestSendmuxJMAPRecovery(t *testing.T) {
	for _, failure := range []string{"", "Email/set", "EmailSubmission/set", "stateMismatch"} {
		t.Run(failure, func(t *testing.T) {
			s, f := newJMAPFixture(t)
			if failure == "stateMismatch" {
				f.conflict = true
			} else {
				f.lose = failure
			}
			request := jmapTestReply()
			key := "dearmachine-control-test"
			id, err := s.Send(context.Background(), "machine@example.test", request, key)
			if failure != "" && failure != "stateMismatch" {
				if err == nil {
					t.Fatal("lost response reported successful")
				}
				id, err = s.Send(context.Background(), "machine@example.test", request, key)
			}
			if err != nil || id != "email" {
				t.Fatalf("send: %s %v", id, err)
			}
			for i := 0; i < 2; i++ {
				if _, err = s.Send(context.Background(), "machine@example.test", request, key); err != nil {
					t.Fatal(err)
				}
			}
			if f.submissions != 1 {
				t.Fatalf("%d submissions", f.submissions)
			}
			if f.email["mailboxIds"].(map[string]any)["sent"] != true {
				t.Fatal("missing Sent receipt")
			}
			if got := f.email["inReplyTo"].([]any)[0]; got != "parent@example.test" {
				t.Fatal("lost reply ancestry")
			}
			body := f.email["bodyValues"].(map[string]any)["text"].(map[string]any)["value"].(string)
			if len(approvalReferences(Message{Body: body})) != 1 || strings.Contains(body, "owner@example.test") {
				t.Fatal("invalid private correlation footer")
			}
			if _, ok := f.email["bodyValues"].(map[string]any)["html"]; ok {
				t.Fatal("private control has divergent HTML")
			}
			request.CC = []string{"guest@example.test"}
			if _, err = s.Send(context.Background(), "machine@example.test", request, key); err == nil {
				t.Fatal("changed recipient envelope reused idempotency key")
			}
			if f.submissions != 1 {
				t.Fatal("changed request sent")
			}
		})
	}
}
func TestSendmuxJMAPPublicReplyHasNoApprovalReference(t *testing.T) {
	s, f := newJMAPFixture(t)
	request := jmapTestReply()
	request.CC = []string{"guest@example.test"}
	request.Files = []sendmuxSendFile{{Filename: "result.txt", ContentType: "text/plain", Content: "YWJj"}}
	if _, err := s.Send(context.Background(), "machine@example.test", request, "public-reply"); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(f.email)
	if strings.Contains(string(encoded), approvalReferencePrefix) {
		t.Fatal("private reference leaked to guest")
	}
	if f.email["bodyStructure"].(map[string]any)["type"] != "multipart/mixed" {
		t.Fatal("attachment missing")
	}
	if f.email["cc"].([]any)[0].(map[string]any)["email"] != "guest@example.test" {
		t.Fatal("lost guest CC")
	}
}
func TestSendmuxJMAPRejectsForeignCredentialDestination(t *testing.T) {
	s, f := newJMAPFixture(t)
	f.foreign = true
	if _, err := s.Send(context.Background(), "machine@example.test", jmapTestReply(), "key"); err == nil || !strings.Contains(err.Error(), "credential destination") {
		t.Fatalf("foreign API: %v", err)
	}
	if f.email != nil || f.submissions != 0 {
		t.Fatal("mutated mailbox")
	}
}

func TestSendmuxDeliveryDoesNotTreatQueuedAsDelivered(t *testing.T) {
	for _, mode := range []string{"unknown", "yes", "no"} {
		t.Run(mode, func(t *testing.T) {
			s, f := newJMAPFixture(t)
			f.submissions = 1
			f.delivered = mode
			f.undo = "pending"
			result, err := s.delivery(context.Background(), "machine@example.test", "email")
			if err != nil {
				t.Fatal(err)
			}
			expected := map[string]string{"unknown": "unconfirmed", "yes": "delivered", "no": "failed"}[mode]
			if result.Status != "queued; delivery unconfirmed" || len(result.Recipients) != 1 || result.Recipients[0].Status != expected {
				t.Fatalf("delivery: %+v", result)
			}
		})
	}
}

func TestSendmuxJMAPConcurrentRecoverySubmitsOnce(t *testing.T) {
	s, f := newJMAPFixture(t)
	var wg sync.WaitGroup
	failures := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Send(context.Background(), "machine@example.test", jmapTestReply(), "same-key")
			failures <- err
		}()
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.submissions != 1 {
		t.Fatalf("concurrent recovery made %d submissions", f.submissions)
	}
}
