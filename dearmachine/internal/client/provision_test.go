package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"sendmux.ai/go/management"
)

func TestProvisionOpenMailInboxAndAuthorizePair(t *testing.T) {
	var rules []openMailPolicyRule
	var modes []openMailPolicyMode
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer offline-openmail-key" {
			t.Fatalf("authorization header missing")
		}
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/inboxes":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || len(body) != 0 {
				t.Fatalf("create body = %#v, %v", body, err)
			}
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(openMailInbox{ID: "inb-created", Address: "created@openmail.sh"})
		case request.Method == http.MethodGet && request.URL.Path == "/v1/inboxes/inb-created":
			_ = json.NewEncoder(writer).Encode(openMailInbox{ID: "inb-created", Address: "created@openmail.sh"})
		case request.Method == http.MethodPost && request.URL.Path == "/v1/policy/rules":
			if request.URL.Query().Get("inboxId") != "inb-created" {
				t.Fatalf("policy inbox = %q", request.URL.RawQuery)
			}
			var rule openMailPolicyRule
			if err := json.NewDecoder(request.Body).Decode(&rule); err != nil {
				t.Fatal(err)
			}
			rules = append(rules, rule)
			writer.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(writer).Encode(map[string]bool{"ok": true})
		case request.Method == http.MethodPut && request.URL.Path == "/v1/policy/mode":
			if request.URL.Query().Get("inboxId") != "inb-created" {
				t.Fatalf("policy inbox = %q", request.URL.RawQuery)
			}
			var mode openMailPolicyMode
			if err := json.NewDecoder(request.Body).Decode(&mode); err != nil {
				t.Fatal(err)
			}
			modes = append(modes, mode)
			_ = json.NewEncoder(writer).Encode(map[string]bool{"ok": true})
		default:
			t.Fatalf("unexpected OpenMail request: %s %s", request.Method, request.URL.String())
		}
	}))
	defer server.Close()

	inbox, err := provisionOpenMailInbox(context.Background(), openMailTransportConfig{
		BaseURL: server.URL, APIKey: "offline-openmail-key", HTTPClient: server.Client(),
	})
	if err != nil {
		t.Fatalf("provisionOpenMailInbox: %v", err)
	}
	if inbox.Transport != "openmail" || inbox.ProviderID != "inb-created" || inbox.Address != "created@openmail.sh" {
		t.Fatalf("inbox = %+v", inbox)
	}
	transport, err := newOpenMailTransport(openMailTransportConfig{
		BaseURL: server.URL, APIKey: "offline-openmail-key", Inbox: inbox.ProviderID,
		HTTPClient: server.Client(), AllowMutation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.AuthorizePair(context.Background(), "Pair <PAIR@Example.test>"); err != nil {
		t.Fatalf("AuthorizePair: %v", err)
	}
	wantRules := []openMailPolicyRule{
		{Type: "allow", Value: "pair@example.test", Direction: "inbound"},
		{Type: "allow", Value: "pair@example.test", Direction: "outbound"},
	}
	wantModes := []openMailPolicyMode{
		{Mode: "allowlist", Direction: "inbound"},
		{Mode: "allowlist", Direction: "outbound"},
	}
	if !slices.Equal(rules, wantRules) || !slices.Equal(modes, wantModes) {
		t.Fatalf("policy rules/modes = %+v/%+v", rules, modes)
	}
}

type fakeSendmuxManagementAPI struct {
	createdAddress string
	requested      string
	deleted        []string
	filters        sendmuxFilterState
	setFilters     []sendmuxFilterState
}

func (fake *fakeSendmuxManagementAPI) CreateMailbox(_ context.Context, address string) (sendmuxProvisionedMailbox, error) {
	fake.requested = address
	fake.createdAddress = "dearmachine-fixed@myagent.mx"
	return sendmuxProvisionedMailbox{
		ID: "mbx-created", Email: fake.createdAddress, Status: "active",
		Credential: "smx_mbx_offline-created-credential",
	}, nil
}

func (fake *fakeSendmuxManagementAPI) DeleteMailbox(_ context.Context, id string) error {
	fake.deleted = append(fake.deleted, id)
	return nil
}

func (fake *fakeSendmuxManagementAPI) GetMailboxFilters(context.Context, string) (sendmuxFilterState, error) {
	return fake.filters, nil
}

func (fake *fakeSendmuxManagementAPI) SetMailboxFilters(_ context.Context, _ string, state sendmuxFilterState) error {
	fake.filters = state
	fake.setFilters = append(fake.setFilters, state)
	return nil
}

func TestProvisionSendmuxInboxStoresScopedCredentialAndAuthorizesPair(t *testing.T) {
	home := t.TempDir()
	fake := &fakeSendmuxManagementAPI{filters: sendmuxFilterState{
		Mode: "off", Rules: []sendmuxFilterRule{{Type: "allow", Pattern: "existing@example.test"}}, Revision: "v1",
	}}
	inbox, err := provisionSendmuxInbox(context.Background(), sendmuxProvisionConfig{
		API: fake, UserHomeDir: func() (string, error) { return home, nil },
		RandomSuffix: func() (string, error) { return "fixed", nil },
	})
	if err != nil {
		t.Fatalf("provisionSendmuxInbox: %v", err)
	}
	if inbox.Transport != "sendmux" || inbox.ProviderID != "mbx-created" || inbox.Address != fake.createdAddress {
		t.Fatalf("inbox = %+v", inbox)
	}
	if fake.requested != "dearmachine-fixed@myagent.mx" {
		t.Fatalf("requested mailbox address = %q", fake.requested)
	}
	credentialPath, err := sendmuxStoredCredentialPath(func() (string, error) { return home, nil }, inbox.ProviderID)
	if err != nil {
		t.Fatal(err)
	}
	credentialInfo, err := os.Stat(credentialPath)
	if err != nil || credentialInfo.Mode().Perm() != 0o600 {
		t.Fatalf("credential mode = %v, %v", credentialInfo, err)
	}
	directoryInfo, err := os.Stat(filepath.Dir(credentialPath))
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("credential directory mode = %v, %v", directoryInfo, err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SENDMUX_MAILBOX_API_KEY", "")
	t.Setenv("SENDMUX_MAILBOX_API_KEY_FILE", "")
	credential, err := loadSendmuxCredential(inbox.ProviderID)
	if err != nil || credential != "smx_mbx_offline-created-credential" {
		t.Fatalf("stored credential = %q, %v", credential, err)
	}
	credential, err = loadSendmuxCredential(inbox.Address)
	if err != nil || credential != "smx_mbx_offline-created-credential" {
		t.Fatalf("stored credential by address = %q, %v", credential, err)
	}

	if err := authorizeSendmuxPair(context.Background(), fake, inbox.ProviderID, "Pair <PAIR@Example.test>"); err != nil {
		t.Fatalf("authorizeSendmuxPair: %v", err)
	}
	if len(fake.setFilters) != 1 || fake.filters.Mode != "allowlist" || !slices.Equal(fake.filters.Rules, []sendmuxFilterRule{
		{Type: "allow", Pattern: "existing@example.test"},
		{Type: "allow", Pattern: "pair@example.test"},
	}) {
		t.Fatalf("filters = %+v", fake.filters)
	}
	if err := authorizeSendmuxPair(context.Background(), fake, inbox.ProviderID, "pair@example.test"); err != nil {
		t.Fatal(err)
	}
	if len(fake.setFilters) != 1 {
		t.Fatalf("idempotent authorization rewrote filters: %+v", fake.setFilters)
	}
}

func TestProvisionSendmuxInboxDeletesMailboxWhenCredentialCannotBeStored(t *testing.T) {
	root := t.TempDir()
	homeFile := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(homeFile, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSendmuxManagementAPI{}
	_, err := provisionSendmuxInbox(context.Background(), sendmuxProvisionConfig{
		API: fake, UserHomeDir: func() (string, error) { return homeFile, nil },
		RandomSuffix: func() (string, error) { return "fixed", nil },
	})
	if err == nil {
		t.Fatal("provisionSendmuxInbox succeeded with unusable credential home")
	}
	if !slices.Equal(fake.deleted, []string{"mbx-created"}) {
		t.Fatalf("rollback deletes = %v", fake.deleted)
	}
}

func TestSendmuxManagementCredentialIsDistinctFromMailboxCredential(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	rootPath := filepath.Join(home, "root-key")
	if err := os.WriteFile(rootPath, []byte("smx_root_offline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SENDMUX_API_KEY", "")
	t.Setenv("SENDMUX_API_KEY_FILE", rootPath)
	credential, configured, err := loadSendmuxManagementCredential()
	if err != nil || !configured || credential != "smx_root_offline" {
		t.Fatalf("management credential = %q/%v/%v", credential, configured, err)
	}
	t.Setenv("SENDMUX_API_KEY_FILE", "")
	if _, configured, err := loadSendmuxManagementCredential(); err != nil || configured {
		t.Fatalf("optional management credential = %v/%v", configured, err)
	}
}

func TestSendmuxManagementCredentialUsesInfrastructureKeyDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("SENDMUX_API_KEY", "")
	t.Setenv("SENDMUX_API_KEY_FILE", "")
	credentialPath := filepath.Join(home, ".config", "dearmachine", "sendmux-infrastructure-api-key")
	if err := os.MkdirAll(filepath.Dir(credentialPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(credentialPath, []byte("smx_root_offline\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	credential, configured, err := loadSendmuxManagementCredential()
	if err != nil || !configured || credential != "smx_root_offline" {
		t.Fatalf("default infrastructure credential = %q/%v/%v", credential, configured, err)
	}
}

func TestSendmuxCreateMailboxMintsCredentialWhenInitialCredentialIsUnavailable(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/mailboxes":
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"meta":{"request_id":"req-create"},"ok":true,"data":{"mailbox":{"created_at":"2026-08-31T00:00:00Z","display_name":null,"email":"dearmachine-fixed@myagent.mx","id":"mbx-created","quota_bytes":null,"send_scope":null,"status":"active"},"credential":null,"warning":"initial credential unavailable"}}`))
		case "/mailboxes/mbx-created/keys":
			var body management.ManagementCreateMailboxKeyReq
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.GetAppName() != "DearMachine" {
				t.Fatalf("credential request = %+v, %v", body, err)
			}
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"meta":{"request_id":"req-key"},"ok":true,"data":{"credential":{"imap_port":993,"key_prefix":"smx_mbx_","key_suffix":"last","public_id":"key-created","secret":"smx_mbx_offline-created-credential","server":"mail.sendmux.test","smtp_port":465,"username":"dearmachine-fixed@myagent.mx"}}}`))
		default:
			t.Fatalf("unexpected Sendmux request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	sdk, err := management.New("smx_root_offline", management.WithBaseURL(server.URL), management.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	created, err := (&sendmuxSDKManagementAPI{client: sdk}).CreateMailbox(context.Background(), "dearmachine-fixed@myagent.mx")
	if err != nil {
		t.Fatal(err)
	}
	if created.ID != "mbx-created" || created.Email != "dearmachine-fixed@myagent.mx" || created.Status != "active" || created.Credential != "smx_mbx_offline-created-credential" {
		t.Fatalf("created mailbox = %+v", created)
	}
	if !slices.Equal(requests, []string{"POST /mailboxes", "POST /mailboxes/mbx-created/keys"}) {
		t.Fatalf("requests = %v", requests)
	}
}

func TestSendmuxCreateMailboxRollsBackWhenCredentialMintFails(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)
		writer.Header().Set("Content-Type", "application/json")
		switch request.Method + " " + request.URL.Path {
		case "POST /mailboxes":
			writer.WriteHeader(http.StatusCreated)
			_, _ = writer.Write([]byte(`{"meta":{"request_id":"req-create"},"ok":true,"data":{"mailbox":{"created_at":"2026-08-31T00:00:00Z","display_name":null,"email":"dearmachine-fixed@myagent.mx","id":"mbx-created","quota_bytes":null,"send_scope":null,"status":"active"},"credential":null,"warning":"initial credential unavailable"}}`))
		case "POST /mailboxes/mbx-created/keys":
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"meta":{"request_id":"req-key"},"ok":false,"error":{"code":"service_unavailable","message":"try later"}}`))
		case "DELETE /mailboxes/mbx-created":
			_, _ = writer.Write([]byte(`{"meta":{"request_id":"req-delete"},"ok":true,"data":{"deleted":true}}`))
		default:
			t.Fatalf("unexpected Sendmux request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()

	sdk, err := management.New("smx_root_offline", management.WithBaseURL(server.URL), management.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&sendmuxSDKManagementAPI{client: sdk}).CreateMailbox(context.Background(), "dearmachine-fixed@myagent.mx")
	if err == nil {
		t.Fatal("CreateMailbox succeeded after credential mint failed")
	}
	if !slices.Equal(requests, []string{"POST /mailboxes", "POST /mailboxes/mbx-created/keys", "DELETE /mailboxes/mbx-created"}) {
		t.Fatalf("requests = %v", requests)
	}
}

func TestProvisionSendmuxInboxPropagatesCreateFailureWithoutRollback(t *testing.T) {
	fake := &failingSendmuxManagementAPI{err: errors.New("create failed")}
	_, err := provisionSendmuxInbox(context.Background(), sendmuxProvisionConfig{
		API: fake, UserHomeDir: func() (string, error) { return t.TempDir(), nil },
		RandomSuffix: func() (string, error) { return "fixed", nil },
	})
	if err == nil || !errors.Is(err, fake.err) || len(fake.deleted) != 0 {
		t.Fatalf("create failure = %v, deletes = %v", err, fake.deleted)
	}
}

type failingSendmuxManagementAPI struct {
	err     error
	deleted []string
}

func (fake *failingSendmuxManagementAPI) CreateMailbox(context.Context, string) (sendmuxProvisionedMailbox, error) {
	return sendmuxProvisionedMailbox{}, fake.err
}
func (fake *failingSendmuxManagementAPI) DeleteMailbox(_ context.Context, id string) error {
	fake.deleted = append(fake.deleted, id)
	return nil
}
func (*failingSendmuxManagementAPI) GetMailboxFilters(context.Context, string) (sendmuxFilterState, error) {
	return sendmuxFilterState{}, errors.New("unused")
}
func (*failingSendmuxManagementAPI) SetMailboxFilters(context.Context, string, sendmuxFilterState) error {
	return errors.New("unused")
}
