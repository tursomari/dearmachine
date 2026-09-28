package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/dearmachine/dearmachine/internal/client"
	"github.com/dearmachine/dearmachine/internal/supervisor"
)

// Deliberately separate from the durable record: never expose its token,
// message body, preview, recipients, or files through native status.
type outboundApprovalStatus struct {
	PairID     string `json:"pairId"`
	SendKey    string `json:"sendKey"`
	Revision   int    `json:"revision"`
	MessageID  string `json:"messageId"`
	ThreadID   string `json:"threadId"`
	State      string `json:"state"`
	HoldReason string `json:"holdReason"`
}

type outboundStatus struct {
	Approvals []outboundApprovalStatus `json:"approvals"`
	Notices   []outboundNoticeStatus   `json:"notices"`
	// Generic errors only: database errors can quote private record contents.
	Unavailable []string `json:"unavailablePairs,omitempty"`
	Error       string   `json:"error,omitempty"`
}

type outboundNoticeStatus struct {
	PairID     string `json:"pairId"`
	MessageID  string `json:"messageId"`
	ThreadID   string `json:"threadId"`
	State      string `json:"state"`
	HoldReason string `json:"holdReason"`
}

func observeOutboundStatus(home func() (string, error)) outboundStatus {
	result := outboundStatus{Approvals: []outboundApprovalStatus{}, Notices: []outboundNoticeStatus{}}
	path, err := client.DefaultPairRegistryPath(home)
	if err != nil {
		result.Error = "cannot resolve pair registry"
		return result
	}
	registry, err := client.LoadPairRegistry(path)
	if err != nil {
		result.Error = "cannot read pair registry"
		return result
	}
	for _, pair := range registry.Pairs {
		path, err := client.DefaultPairDatabasePath(home, pair.ID)
		if err != nil {
			result.Unavailable = append(result.Unavailable, pair.ID)
			continue
		}
		approvals, approvalErr := readOutboundStatus(path, pair.ID)
		result.Approvals = append(result.Approvals, approvals...)
		notices, err := readOutboundNoticeStatus(path, pair.ID)
		if err != nil || approvalErr != nil {
			result.Unavailable = append(result.Unavailable, pair.ID)
		}
		result.Notices = append(result.Notices, notices...)
	}
	return result
}

func readOutboundNoticeStatus(path, pairID string) ([]outboundNoticeStatus, error) {
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_query_only=1&_busy_timeout=1000"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var exists int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='outbound_notices'`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, nil
	}
	rows, err := db.Query(`SELECT message_id, thread_id, state, hold_reason FROM outbound_notices WHERE state IN ('held', 'sending') ORDER BY notice_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []outboundNoticeStatus
	for rows.Next() {
		s := outboundNoticeStatus{PairID: pairID}
		if err := rows.Scan(&s.MessageID, &s.ThreadID, &s.State, &s.HoldReason); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func readOutboundStatus(path, pairID string) ([]outboundApprovalStatus, error) {
	// Do not use OpenPairStore: status must not create databases or migrate them.
	u := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_query_only=1&_busy_timeout=1000"}
	db, err := sql.Open("sqlite3", u.String())
	if err != nil {
		return nil, err
	}
	defer db.Close()
	var exists int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='outbound_approvals'`).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, nil // Databases predating outbound approval have no holds.
	}
	// Select only allowlisted metadata from the reference projection. Older
	// databases without that projection are unavailable until the client migrates
	// them; status never falls back to loading payload-bearing record JSON.
	rows, err := db.Query(`SELECT o.send_key, o.revision,
	 json_extract(o.reference, '$.Message.MessageID'),
	 json_extract(o.reference, '$.Message.ThreadID'),
	 json_extract(o.reference, '$.State'),
	 coalesce(json_extract(o.reference, '$.HoldReason'), '')
	 FROM outbound_approvals o
	 WHERE o.revision = (SELECT max(n.revision) FROM outbound_approvals n WHERE n.send_key=o.send_key)
	 AND o.state NOT IN ('sent', 'rejected', 'superseded')
	 ORDER BY o.send_key, o.revision`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []outboundApprovalStatus
	for rows.Next() {
		s := outboundApprovalStatus{PairID: pairID}
		if err := rows.Scan(&s.SendKey, &s.Revision, &s.MessageID, &s.ThreadID, &s.State, &s.HoldReason); err != nil {
			return nil, err
		}
		result = append(result, s)
	}
	return result, rows.Err()
}

func writeOutboundStatus(w io.Writer, s outboundStatus) error {
	if _, err := fmt.Fprintf(w, "\nOutbound approvals: %d unresolved (not confirmation of delivery)\n", len(s.Approvals)); err != nil {
		return err
	}
	for _, a := range s.Approvals {
		if _, err := fmt.Fprintf(w, "  Pair %s; request %q; thread %q; revision %d\n    State: %s\n    Hold reason: %q\n", a.PairID, a.MessageID, a.ThreadID, a.Revision, a.State, a.HoldReason); err != nil {
			return err
		}
	}
	for _, n := range s.Notices {
		if _, err := fmt.Fprintf(w, "  Owner notice; pair %s; request %q; thread %q\n    State: %s\n    Hold reason: %q\n", n.PairID, n.MessageID, n.ThreadID, n.State, n.HoldReason); err != nil {
			return err
		}
	}
	for _, pair := range s.Unavailable {
		if _, err := fmt.Fprintf(w, "  Pair %s: outbound approval status unavailable\n", pair); err != nil {
			return err
		}
	}
	if s.Error != "" {
		_, err := fmt.Fprintf(w, "  Outbound approval status unavailable: %s\n", s.Error)
		return err
	}
	return nil
}

func writeStatusJSON(w io.Writer, s supervisor.Status, outbound outboundStatus) error {
	s.LastExit = ""
	type nativeStatus struct {
		supervisor.Status
		Outbound outboundStatus `json:"outboundApprovals"`
	}
	return json.NewEncoder(w).Encode(struct {
		Version int          `json:"version"`
		OK      bool         `json:"ok"`
		Status  nativeStatus `json:"status"`
	}{Version: 1, OK: true, Status: nativeStatus{s, outbound}})
}
