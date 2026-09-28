package main

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dearmachine/dearmachine/internal/client"
)

func statusDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	u := url.URL{Scheme: "file", Path: path}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func statusExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func insertStatusApproval(t *testing.T, db *sql.DB, key string, revision int, state, reason string) {
	t.Helper()
	metadata, err := json.Marshal(map[string]any{
		"Key": key, "Revision": revision, "State": state, "HoldReason": reason,
		"PairID": "old-pair", "Owner": "private-owner@example.test", "Token": "SECRET-TOKEN",
		"Message": map[string]any{"MessageID": "request-1", "ThreadID": "thread-1", "Body": "SECRET-BODY"},
		"Payload": map[string]any{"Text": "SECRET-PAYLOAD", "Attachments": []string{"SECRET-FILE"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Invalid full record proves the status reader never parses payload records.
	statusExec(t, db, `INSERT INTO outbound_approvals(send_key,revision,token,record,reference,state) VALUES(?,?,?,?,?,?)`, key, revision, key+string(rune(revision)), "not JSON SECRET-RECORD", string(metadata), state)
}

func TestOutboundStatusCLIShowsMetadataAndNoticeHolds(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	pair := makeUpTestPair(t, deps, "outbound-status")
	path, err := client.DefaultPairDatabasePath(deps.userHomeDir, pair.ID)
	if err != nil {
		t.Fatal(err)
	}
	db := statusDB(t, path)
	insertStatusApproval(t, db, "preview", 1, "preview_sending", "preview retries exhausted")
	insertStatusApproval(t, db, "submission", 1, "sending", "submission receipt ambiguous")
	insertStatusApproval(t, db, "scope", 1, "approved", "outbound scope mismatch")
	insertStatusApproval(t, db, "pending", 1, "pending", "")
	insertStatusApproval(t, db, "revised", 1, "superseded", "OLD-REASON")
	insertStatusApproval(t, db, "revised", 2, "pending", "")
	for _, state := range []string{"sent", "rejected", "superseded"} {
		insertStatusApproval(t, db, state, 1, state, "TERMINAL-REASON")
	}
	statusExec(t, db, `CREATE TABLE IF NOT EXISTS outbound_notices(notice_key TEXT PRIMARY KEY, pair_id TEXT NOT NULL, inbox_id TEXT NOT NULL, owner TEXT NOT NULL, message_id TEXT NOT NULL, thread_id TEXT NOT NULL, state TEXT NOT NULL, hold_reason TEXT NOT NULL, receipt TEXT NOT NULL)`)
	for _, state := range []string{"held", "sending", "sent"} {
		statusExec(t, db, `INSERT INTO outbound_notices VALUES(?,?,?,?,?,?,?,?,?)`, "SECRET-NOTICE-KEY-"+state, pair.ID, "inbox", "private-owner@example.test", "notice-request", "notice-thread", state, "notice delivery uncertain", "SECRET-RECEIPT")
	}
	var output strings.Builder
	deps.stdout = &output
	if err := runStatus([]string{"--json"}, deps); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Version int  `json:"version"`
		OK      bool `json:"ok"`
		Status  struct {
			Outbound outboundStatus `json:"outboundApprovals"`
		} `json:"status"`
	}
	if err := json.Unmarshal([]byte(output.String()), &response); err != nil {
		t.Fatal(err)
	}
	got := response.Status.Outbound
	if response.Version != 1 || !response.OK || len(got.Approvals) != 5 || len(got.Notices) != 2 || len(got.Unavailable) != 0 || got.Error != "" {
		t.Fatalf("status: %s", output.String())
	}
	for _, a := range got.Approvals {
		if a.PairID != pair.ID || a.MessageID != "request-1" || a.ThreadID != "thread-1" {
			t.Fatalf("scope: %+v", a)
		}
		if a.SendKey == "revised" && a.Revision != 2 {
			t.Fatalf("stale revision: %+v", a)
		}
	}
	checkStatusPrivacy(t, output.String())
	// Test the normal CLI and the detailed view, not just the renderer.
	for _, args := range [][]string{nil, {"--details"}} {
		output.Reset()
		if err := runStatus(args, deps); err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"5 unresolved", "State: preview_sending", "preview retries exhausted", "State: sending", "submission receipt ambiguous", "outbound scope mismatch", "Owner notice", "State: held", "notice delivery uncertain"} {
			if !strings.Contains(output.String(), want) {
				t.Fatalf("missing %q: %s", want, output.String())
			}
		}
		checkStatusPrivacy(t, output.String())
	}
}

func checkStatusPrivacy(t *testing.T, output string) {
	t.Helper()
	for _, secret := range []string{"SECRET-", "private-owner@", "OLD-REASON", "TERMINAL-REASON", "old-pair"} {
		if strings.Contains(output, secret) {
			t.Fatalf("status leaked %q: %s", secret, output)
		}
	}
}

func TestOutboundStatusUnavailablePairDoesNotHideOtherPairs(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	broken := makeUpTestPair(t, deps, "broken-status")
	healthy := makeUpTestPair(t, deps, "healthy-status")
	brokenPath, _ := client.DefaultPairDatabasePath(deps.userHomeDir, broken.ID)
	healthyPath, _ := client.DefaultPairDatabasePath(deps.userHomeDir, healthy.ID)
	insertStatusApproval(t, statusDB(t, healthyPath), "held", 1, "sending", "retries exhausted")
	if err := os.Remove(brokenPath); err != nil {
		t.Fatal(err)
	}
	got := observeOutboundStatus(deps.userHomeDir)
	if len(got.Unavailable) != 1 || got.Unavailable[0] != broken.ID || len(got.Approvals) != 1 || got.Approvals[0].PairID != healthy.ID {
		t.Fatalf("status: %+v", got)
	}
	if _, err := os.Stat(brokenPath); !os.IsNotExist(err) {
		t.Fatalf("status created missing database: %v", err)
	}
	var output strings.Builder
	if err := writeOutboundStatus(&output, got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "status unavailable") || !strings.Contains(output.String(), "retries exhausted") {
		t.Fatal(output.String())
	}
}

func TestOutboundStatusReadOnlyLegacyAndMalformed(t *testing.T) {
	for _, kind := range []string{"pre-outbound", "pre-projection", "malformed"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state ? #.db")
			db := statusDB(t, path)
			switch kind {
			case "pre-outbound":
				statusExec(t, db, `CREATE TABLE legacy(id TEXT)`)
			case "pre-projection":
				statusExec(t, db, `CREATE TABLE outbound_approvals(send_key TEXT, revision INTEGER, record TEXT)`)
			case "malformed":
				statusExec(t, db, `CREATE TABLE outbound_approvals(send_key TEXT, revision INTEGER, reference TEXT, state TEXT)`)
				statusExec(t, db, `INSERT INTO outbound_approvals VALUES('key',1,'SECRET-BROKEN-JSON','sending')`)
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			_, err = readOutboundStatus(path, "pair")
			if (err != nil) != (kind != "pre-outbound") {
				t.Fatalf("error = %v", err)
			}
			if notices, err := readOutboundNoticeStatus(path, "pair"); err != nil || len(notices) != 0 {
				t.Fatalf("old notice table: %+v %v", notices, err)
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("status mutated database")
			}
		})
	}
}

func TestOutboundStatusMalformedApprovalStillShowsNotice(t *testing.T) {
	deps := testDependencies(t, &fakeApplication{})
	pair := makeUpTestPair(t, deps, "malformed-status")
	path, _ := client.DefaultPairDatabasePath(deps.userHomeDir, pair.ID)
	db := statusDB(t, path)
	insertStatusApproval(t, db, "broken", 1, "sending", "unused")
	statusExec(t, db, `UPDATE outbound_approvals SET reference='SECRET-BROKEN-JSON'`)
	statusExec(t, db, `CREATE TABLE IF NOT EXISTS outbound_notices(notice_key TEXT PRIMARY KEY, pair_id TEXT NOT NULL, inbox_id TEXT NOT NULL, owner TEXT NOT NULL, message_id TEXT NOT NULL, thread_id TEXT NOT NULL, state TEXT NOT NULL, hold_reason TEXT NOT NULL, receipt TEXT NOT NULL)`)
	statusExec(t, db, `INSERT INTO outbound_notices VALUES('key',?,'inbox','private-owner@example.test','request','thread','held','notice retries exhausted','SECRET-RECEIPT')`, pair.ID)
	var output strings.Builder
	deps.stdout = &output
	if err := runStatus([]string{"--json"}, deps); err != nil {
		t.Fatal(err)
	}
	checkStatusPrivacy(t, output.String())
	if !strings.Contains(output.String(), `"unavailablePairs"`) || !strings.Contains(output.String(), "notice retries exhausted") {
		t.Fatal(output.String())
	}
}
