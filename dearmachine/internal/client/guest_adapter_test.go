package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
)

func TestGuestAgentMailReceiveOnlyAndExactOwnership(t *testing.T) {
	present := false
	created := "2026-09-12T00:00:00Z"
	deletes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasPrefix(r.URL.Path, "/v0/inboxes/inbox/lists/receive/allow") {
			t.Errorf("guest broadened provider permissions: %s", r.URL.Path)
			w.WriteHeader(400)
			return
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
			deletes++
			present = false
			w.WriteHeader(204)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"created_at": created, "entry": "guest@example.test", "entry_type": "email", "direction": "receive", "list_type": "allow", "inbox_id": "inbox"})
	}))
	defer srv.Close()
	m, _ := NewMailbox(agentmail.NewClient(option.WithBaseURL(srv.URL+"/"), option.WithAPIKey("offline"), option.WithMaxRetries(0)), "inbox")
	p, err := m.AddReceive(context.Background(), "guest@example.test")
	if err != nil || !p.Present || p.Token == "" {
		t.Fatalf("add=%+v %v", p, err)
	}
	created = "2026-09-12T00:00:01Z"
	if err = m.RemoveReceive(context.Background(), "guest@example.test", p.Token); err != nil {
		t.Fatal(err)
	}
	if deletes != 0 {
		t.Fatal("removed externally replaced rule")
	}
	p, err = m.InspectReceive(context.Background(), "guest@example.test")
	if err != nil {
		t.Fatal(err)
	}
	if err = m.RemoveReceive(context.Background(), "guest@example.test", p.Token); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 {
		t.Fatalf("deletes=%d", deletes)
	}
}

func TestGuestOpenMailReceiveOnlyAndExactRuleID(t *testing.T) {
	present := false
	deletes := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/inboxes/inbox":
			json.NewEncoder(w).Encode(openMailInbox{ID: "inbox", Address: "machine@example.test"})
		case r.Method == "GET" && r.URL.Path == "/v1/policy":
			if r.URL.Query().Get("inboxId") != "inbox" {
				t.Error("policy escaped inbox")
			}
			rules := []map[string]string{}
			if present {
				rules = append(rules, map[string]string{"id": "owned-rule", "direction": "inbound", "type": "allow", "value": "guest@example.test"})
			}
			json.NewEncoder(w).Encode(map[string]any{"rules": rules, "inheritedRules": []map[string]string{{"id": "inherited", "type": "allow", "value": "other@example.test"}}})
		case r.Method == "POST" && r.URL.Path == "/v1/policy/rules":
			var rule openMailPolicyRule
			json.NewDecoder(r.Body).Decode(&rule)
			if rule.Direction != "inbound" || rule.Value != "guest@example.test" || r.URL.Query().Get("inboxId") != "inbox" {
				t.Errorf("guest policy=%+v", rule)
			}
			present = true
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]string{"id": "owned-rule"})
		case r.Method == "DELETE" && r.URL.Path == "/v1/policy/rules/owned-rule":
			if r.URL.Query().Get("inboxId") != "inbox" {
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(map[string]string{"code": "inherited_rule"})
				return
			}
			present = false
			deletes++
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(400)
		}
	}))
	defer srv.Close()
	m, err := newOpenMailTransport(openMailTransportConfig{BaseURL: srv.URL, APIKey: "offline", Inbox: "inbox", HTTPClient: srv.Client(), AllowMutation: true})
	if err != nil {
		t.Fatal(err)
	}
	p, err := m.AddReceive(context.Background(), "guest@example.test")
	if err != nil || p.Token != "owned-rule" {
		t.Fatalf("add=%+v %v", p, err)
	}
	if err = m.RemoveReceive(context.Background(), "guest@example.test", "unowned"); err != nil {
		t.Fatal(err)
	}
	if deletes != 0 {
		t.Fatal("deleted unowned rule")
	}
	if err = m.RemoveReceive(context.Background(), "guest@example.test", p.Token); err != nil {
		t.Fatal(err)
	}
	if deletes != 1 {
		t.Fatal("did not remove owned rule")
	}
}

func TestGuestSendmuxUsesLocalAuthorizationWithoutInfrastructureCredential(t *testing.T) {
	t.Setenv("SENDMUX_API_KEY", "")
	t.Setenv("SENDMUX_API_KEY_FILE", "/nonexistent/sendmux-infrastructure-key")
	transport := &SendmuxTransport{}
	permission, err := transport.AddReceive(context.Background(), "guest@example.test")
	if err != nil || !permission.Present || permission.Token != "" {
		t.Fatalf("local authorization = %+v, %v", permission, err)
	}
	if err := transport.RemoveReceive(context.Background(), "guest@example.test", "legacy-owned-filter-token"); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.InspectReceive(context.Background(), "*@example.test"); err == nil {
		t.Fatal("accepted a wildcard identity")
	}
	if err := AuthorizeSendmuxPair(context.Background(), "inbox", "owner@example.test"); err != nil {
		t.Fatal(err)
	}
	if err := AuthorizeSendmuxPair(context.Background(), "inbox", "invalid"); err == nil {
		t.Fatal("accepted invalid owner")
	}
}
