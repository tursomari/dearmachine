package client

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// RemovalToken is an opaque reference to one exact grant generation. It is
// useful only alongside an authenticated decision from that grant's owner.
func (s *GuestStore) RemovalToken(k GuestKey) (string, error) {
	g, err := s.Grant(k)
	if err != nil || !g.Active {
		return "", errors.Join(ErrGuestUnauthorized, err)
	}
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	token := hex.EncodeToString(entropy[:])
	args := append([]any{token}, k.args()...)
	args = append(args, g.Generation)
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO guest_removal_tokens VALUES(?,?,?,?,?,?)`, args...); err != nil {
		return "", err
	}
	err = s.db.QueryRow(`SELECT token FROM guest_removal_tokens WHERE `+grantWhere+` AND generation=?`, append(k.args(), g.Generation)...).Scan(&token)
	return token, err
}

func (s *GuestStore) revokeToken(pairID, inboxID, threadID, messageID, token string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var k GuestKey
	var generation int64
	err = tx.QueryRow(`SELECT pair_id,inbox_id,address,thread_id,generation FROM guest_removal_tokens WHERE token=?`, token).
		Scan(&k.PairID, &k.InboxID, &k.Address, &k.ThreadID, &generation)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if errors.Is(err, sql.ErrNoRows) || k.PairID != pairID || k.InboxID != inboxID || k.ThreadID != threadID || messageID == "" {
		return ErrGuestUnauthorized
	}
	var duplicate bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM guest_removal_receipts WHERE pair_id=? AND inbox_id=? AND message_id=? AND token=?)`, pairID, inboxID, messageID, token).Scan(&duplicate); err != nil {
		return err
	}
	if duplicate {
		return tx.Commit() // An old retry must not revoke a later generation.
	}
	g, err := guestGrant(tx, k)
	if err != nil {
		return err
	}
	if !g.Active || g.Generation != generation {
		return ErrGuestUnauthorized
	}
	if _, err := tx.Exec(`UPDATE guest_grants SET active=0 WHERE `+grantWhere, k.args()...); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE receive_permissions SET pending=1 WHERE inbox_id=? AND address=?`, inboxID, k.Address); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO guest_removal_receipts VALUES(?,?,?,?)`, pairID, inboxID, messageID, token); err != nil {
		return err
	}
	return tx.Commit()
}

func (e *pairEndpoint) guestRemovalFooter(threadID, address string) (string, error) {
	r := e.router
	if r.guests == nil {
		return "", nil
	}
	grants, err := r.guests.List(e.pairID)
	if err != nil {
		return "", err
	}
	var lines []string
	for _, g := range grants {
		if !g.Active || g.InboxID != guestInboxKey(r.inbox) || g.ThreadID != threadID || (address != "" && g.Address != address) {
			continue
		}
		token, err := r.guests.RemovalToken(g.GuestKey)
		if errors.Is(err, ErrGuestUnauthorized) {
			continue
		} else if err != nil {
			return "", err
		}
		lines = append(lines, fmt.Sprintf("To remove %s from this thread, reply with only:\nREMOVE GUEST %s", g.Address, strings.ToUpper(token)))
	}
	if len(lines) == 0 {
		return "", nil
	}
	return "\n\n" + strings.Join(lines, "\n\n"), nil
}

func (a *App) guestApprovalText(message Message, request ParticipantRequest) (string, error) {
	text := participantApprovalPrompt(request)
	e, ok := a.transport.(*pairEndpoint)
	if !ok {
		return text, nil
	}
	foot, err := e.guestRemovalFooter(request.ExternalThreadID, request.ParticipantAddress)
	if err != nil {
		return "", err
	}
	body := authoredControlBody(message)
	if len(body) > 16000 {
		body = body[:16000] + "\n[Preview truncated; review the original guest email.]"
	}
	// The preview is for the owner's review, never an instruction to the agent.
	text += "\n\nGuest message:\n> " + strings.ReplaceAll(body, "\n", "\n> ")
	return text + foot, nil
}

func (a *App) handleGuestRemoval(ctx context.Context, message Message) (bool, error) {
	e, ok := a.transport.(*pairEndpoint)
	if !ok {
		return false, nil
	}
	fields := strings.Fields(authoredControlBody(message))
	if len(fields) < 2 || fields[0] != "REMOVE" || fields[1] != "GUEST" {
		return false, nil
	}
	if !e.isControllingParticipant(message) {
		// Guest control attempts never become approvable agent instructions.
		if err := a.store.RecordControlMessage(message.MessageID, message.ThreadID, ""); err != nil {
			return true, err
		}
		return true, a.transport.MarkProcessed(ctx, message.MessageID)
	}
	seen, err := a.store.Seen(message.MessageID)
	if err != nil || seen {
		if err != nil {
			return true, err
		}
		return true, a.transport.MarkProcessed(ctx, message.MessageID)
	}
	text := "Guest removal was rejected. Reply with the exact REMOVE GUEST command from a private email in this thread."
	if len(fields) == 3 && e.router.guests != nil {
		err = e.router.guests.revokeToken(e.pairID, guestInboxKey(e.router.inbox), message.ThreadID, message.MessageID, strings.ToLower(fields[2]))
		if err == nil {
			text = "Guest removed from this thread. Pending approvals are invalid. Already-started work and submitted email cannot be recalled. Ordinary reply-all messages will not restore permission; an explicit guest allow command is required to invite them again."
			if err := e.syncGuestPermissions(ctx); err != nil {
				text += " Provider permission synchronization is pending; local revocation is already effective."
			}
		} else if !errors.Is(err, ErrGuestUnauthorized) {
			return true, err
		}
	}
	owner := e.controllingParticipant()
	outbound, found, err := a.transport.ReplyReceipt(ctx, message, owner)
	if err != nil {
		return true, err
	}
	if !found {
		outbound, err = a.transport.Reply(ctx, message.MessageID, participantPrivatePayload(text, owner), controlIdempotencyKey("guest-removal", message.MessageID))
		if err != nil {
			return true, err
		}
	}
	if err := a.store.RecordControlMessage(message.MessageID, message.ThreadID, outbound); err != nil {
		return true, err
	}
	return true, a.transport.MarkProcessed(ctx, message.MessageID)
}
