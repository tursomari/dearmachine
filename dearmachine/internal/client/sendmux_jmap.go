package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const sendmuxJMAPOrigin = "https://mail.sendmux.ai"
const sendmuxRequestHashHeader = "header:X-DearMachine-Request-Hash"

type sendmuxJMAPSender struct {
	credential, origin string
	client             *http.Client
}
type sendmuxJMAPSession struct{ Account, Identity, Drafts, Sent, API, Upload string }
type sendmuxJMAPError string

func (e sendmuxJMAPError) Error() string { return "Sendmux mailbox operation failed: " + string(e) }
func newSendmuxJMAPSender(credential string) *sendmuxJMAPSender {
	c := *http.DefaultClient
	c.Timeout = 30 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &sendmuxJMAPSender{credential: credential, origin: sendmuxJMAPOrigin, client: &c}
}
func (s *sendmuxJMAPSender) request(ctx context.Context, address, target, contentType string, body []byte, out any) error {
	u, err := url.Parse(target)
	base, _ := url.Parse(s.origin)
	if err != nil || u.Scheme != base.Scheme || u.Host != base.Host || u.User != nil || u.Fragment != "" {
		return errors.New("refused Sendmux credential destination")
	}
	method := http.MethodGet
	if body != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return errors.New("prepare Sendmux mailbox request failed")
	}
	req.SetBasicAuth(address, s.credential)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Copy even injected clients: redirects must never carry mailbox credentials.
	c := *s.client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := c.Do(req)
	if err != nil {
		return errors.New("Sendmux mailbox request outcome is unknown; retry only through receipt recovery")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Sendmux mailbox HTTP status %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (2<<20)+1))
	if err != nil || len(data) > 2<<20 {
		return errors.New("invalid Sendmux mailbox response")
	}
	if json.Unmarshal(data, out) != nil {
		return errors.New("invalid Sendmux mailbox response")
	}
	return nil
}
func (s *sendmuxJMAPSender) call(ctx context.Context, address string, session sendmuxJMAPSession, method string, args map[string]any, out any) error {
	args["accountId"] = session.Account
	payload, _ := json.Marshal(map[string]any{"using": []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail", "urn:ietf:params:jmap:submission"}, "methodCalls": []any{[]any{method, args, "request"}}})
	var response struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	if err := s.request(ctx, address, session.API, "application/json", payload, &response); err != nil {
		return err
	}
	if len(response.MethodResponses) != 1 || len(response.MethodResponses[0]) != 3 {
		return errors.New("invalid Sendmux method response")
	}
	r := response.MethodResponses[0]
	var name, tag string
	_ = json.Unmarshal(r[0], &name)
	_ = json.Unmarshal(r[2], &tag)
	if tag != "request" {
		return errors.New("mismatched Sendmux method response")
	}
	if name == "error" {
		var failure struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(r[1], &failure)
		if failure.Type == "stateMismatch" {
			return sendmuxJMAPError("stateMismatch")
		}
		return errors.New("Sendmux mailbox method rejected the request")
	}
	if name != method {
		return errors.New("mismatched Sendmux method response")
	}
	var scope struct {
		AccountID string `json:"accountId"`
	}
	_ = json.Unmarshal(r[1], &scope)
	if scope.AccountID != session.Account {
		return errors.New("mismatched Sendmux account")
	}
	if json.Unmarshal(r[1], out) != nil {
		return errors.New("invalid Sendmux method result")
	}
	return nil
}
func (s *sendmuxJMAPSender) session(ctx context.Context, address string) (sendmuxJMAPSession, error) {
	var raw struct {
		Username, APIURL, UploadURL string
		PrimaryAccounts             map[string]string
		Accounts                    map[string]json.RawMessage
		Capabilities                map[string]json.RawMessage
	}
	if err := s.request(ctx, address, s.origin+"/jmap/session", "", nil, &raw); err != nil {
		return sendmuxJMAPSession{}, err
	}
	session := sendmuxJMAPSession{Account: raw.PrimaryAccounts["urn:ietf:params:jmap:mail"], API: raw.APIURL, Upload: raw.UploadURL}
	if !strings.EqualFold(raw.Username, address) || session.Account == "" || raw.Accounts[session.Account] == nil || raw.Capabilities["urn:ietf:params:jmap:submission"] == nil {
		return session, errors.New("Sendmux mailbox submission capability or identity is unavailable")
	}
	var identities struct{ List []struct{ ID, Email string } }
	if err := s.call(ctx, address, session, "Identity/get", map[string]any{}, &identities); err != nil {
		return session, err
	}
	for _, identity := range identities.List {
		if strings.EqualFold(identity.Email, address) {
			if session.Identity != "" {
				return session, errors.New("ambiguous Sendmux sending identity")
			}
			session.Identity = identity.ID
		}
	}
	var folders struct{ List []struct{ ID, Role string } }
	if err := s.call(ctx, address, session, "Mailbox/get", map[string]any{}, &folders); err != nil {
		return session, err
	}
	for _, folder := range folders.List {
		switch folder.Role {
		case "drafts":
			if session.Drafts != "" {
				return session, errors.New("ambiguous Sendmux drafts folder")
			}
			session.Drafts = folder.ID
		case "sent":
			if session.Sent != "" {
				return session, errors.New("ambiguous Sendmux sent folder")
			}
			session.Sent = folder.ID
		}
	}
	if session.Identity == "" || session.Drafts == "" || session.Sent == "" {
		return session, errors.New("Sendmux sending identity or standard folders are missing")
	}
	return session, nil
}
func jmapAddresses(addresses []string) []map[string]string {
	out := make([]map[string]string, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, map[string]string{"email": address})
	}
	return out
}
func (s *sendmuxJMAPSender) email(ctx context.Context, from string, session sendmuxJMAPSession, request sendmuxSendRequest, key, hash string) (map[string]any, error) {
	if request.ParentRFCMessageID == "" {
		return nil, errors.New("Sendmux reply requires the parent's RFC Message-ID")
	}
	text, html := request.Text, request.HTML
	if strings.HasPrefix(request.IdempotencyKey, "dearmachine-control-") && len(request.To) == 1 && len(request.CC) == 0 && len(request.BCC) == 0 {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		footer := "\n\n" + approvalReferencePrefix + hex.EncodeToString(nonce[:])
		text += footer
		// Private controls use plain text, avoiding divergent correlation evidence.
		html = ""
	}
	if len(text)+len(html) > 25<<20 {
		return nil, ErrAttachmentTooLarge
	}
	bodyValues := map[string]any{"text": map[string]any{"value": text}}
	body := map[string]any{"partId": "text", "type": "text/plain"}
	if html != "" {
		bodyValues["html"] = map[string]any{"value": html}
		body = map[string]any{"type": "multipart/alternative", "subParts": []any{body, map[string]any{"partId": "html", "type": "text/html"}}}
	}
	if len(request.Files) > 0 {
		parts := []any{body}
		total := len(text) + len(html)
		for _, file := range request.Files {
			data, err := base64.StdEncoding.DecodeString(file.Content)
			if err != nil {
				return nil, errors.New("invalid Sendmux attachment encoding")
			}
			total += len(data)
			if total > 25<<20 {
				return nil, ErrAttachmentTooLarge
			}
			target := strings.ReplaceAll(session.Upload, "{accountId}", url.PathEscape(session.Account))
			var uploaded struct {
				AccountID, BlobID string
				Size              int
			}
			if err := s.request(ctx, from, target, file.ContentType, data, &uploaded); err != nil {
				return nil, err
			}
			if uploaded.AccountID != session.Account || uploaded.BlobID == "" || uploaded.Size != len(data) {
				return nil, errors.New("invalid Sendmux attachment upload")
			}
			parts = append(parts, map[string]any{"blobId": uploaded.BlobID, "type": file.ContentType, "name": file.Filename, "disposition": "attachment"})
		}
		body = map[string]any{"type": "multipart/mixed", "subParts": parts}
	}
	refs := []string{}
	for _, id := range request.References {
		refs = append(refs, strings.Trim(id, "<>"))
	}
	email := map[string]any{"mailboxIds": map[string]bool{session.Drafts: true}, "keywords": map[string]bool{"$seen": true, "$draft": true, key: true}, "from": jmapAddresses([]string{from}), "to": jmapAddresses(request.To), "cc": jmapAddresses(request.CC), "bcc": jmapAddresses(request.BCC), "subject": request.Subject, "messageId": []string{key + "@" + strings.Split(from, "@")[1]}, "inReplyTo": []string{strings.Trim(request.ParentRFCMessageID, "<>")}, "references": refs, "bodyStructure": body, "bodyValues": bodyValues, sendmuxRequestHashHeader: hash}
	return email, nil
}

// Both creation steps use a state read BEFORE their lookup. A concurrent or
// response-lost mutation changes that state, so a retry cannot submit twice.
func (s *sendmuxJMAPSender) Send(ctx context.Context, from string, request sendmuxSendRequest, idempotencyKey string) (string, error) {
	if _, err := canonicalMessageAddress(from); err != nil {
		return "", err
	}
	if idempotencyKey == "" {
		return "", errors.New("Sendmux submission requires idempotency")
	}
	request.IdempotencyKey = idempotencyKey
	keyHash := sha256.Sum256([]byte(from + "\x00" + idempotencyKey))
	key := fmt.Sprintf("dearmachine-%x", keyHash)
	encoded, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(encoded))
	session, err := s.session(ctx, from)
	if err != nil {
		return "", err
	}
	emailID := ""
	for attempt := 0; attempt < 6; attempt++ {
		var state struct{ State string }
		if err = s.call(ctx, from, session, "Email/get", map[string]any{"ids": []string{}}, &state); err != nil {
			return "", err
		}
		if state.State == "" {
			return "", errors.New("missing Sendmux email state")
		}
		var found struct{ IDs []string }
		if err = s.call(ctx, from, session, "Email/query", map[string]any{"filter": map[string]string{"hasKeyword": key}, "limit": 2}, &found); err != nil {
			return "", err
		}
		if len(found.IDs) > 1 {
			return "", errors.New("ambiguous Sendmux submission receipt")
		}
		if len(found.IDs) == 1 {
			var existing struct{ List []map[string]any }
			if err = s.call(ctx, from, session, "Email/get", map[string]any{"ids": found.IDs, "properties": []string{"id", sendmuxRequestHashHeader}}, &existing); err != nil {
				return "", err
			}
			if len(existing.List) != 1 || existing.List[0]["id"] != found.IDs[0] || strings.TrimSpace(fmt.Sprint(existing.List[0][sendmuxRequestHashHeader])) != hash {
				return "", errors.New("Sendmux idempotency key belongs to a different reply")
			}
			emailID = found.IDs[0]
			break
		}
		email, buildErr := s.email(ctx, from, session, request, key, hash)
		if buildErr != nil {
			return "", buildErr
		}
		var created struct {
			Created    map[string]struct{ ID string }
			NotCreated map[string]json.RawMessage
		}
		err = s.call(ctx, from, session, "Email/set", map[string]any{"ifInState": state.State, "create": map[string]any{"reply": email}}, &created)
		if errors.Is(err, sendmuxJMAPError("stateMismatch")) {
			continue
		}
		if err != nil {
			return "", err
		}
		if len(created.NotCreated) > 0 || created.Created["reply"].ID == "" {
			return "", errors.New("Sendmux could not store the reply")
		}
		emailID = created.Created["reply"].ID
		break
	}
	if emailID == "" {
		return "", errors.New("Sendmux mailbox changed repeatedly; reply remains pending")
	}
	for attempt := 0; attempt < 6; attempt++ {
		var state struct{ State string }
		if err = s.call(ctx, from, session, "EmailSubmission/get", map[string]any{"ids": []string{}}, &state); err != nil {
			return "", err
		}
		if state.State == "" {
			return "", errors.New("missing Sendmux submission state")
		}
		var found struct{ IDs []string }
		if err = s.call(ctx, from, session, "EmailSubmission/query", map[string]any{"filter": map[string]any{"emailIds": []string{emailID}}, "limit": 2}, &found); err != nil {
			return "", err
		}
		if len(found.IDs) > 1 {
			return "", errors.New("ambiguous Sendmux submission")
		}
		if len(found.IDs) == 1 {
			return s.recordSent(ctx, from, session, emailID, key)
		}
		var created struct {
			Created    map[string]struct{ ID string }
			NotCreated map[string]json.RawMessage
		}
		err = s.call(ctx, from, session, "EmailSubmission/set", map[string]any{"ifInState": state.State, "create": map[string]any{"send": map[string]string{"identityId": session.Identity, "emailId": emailID}}}, &created)
		if errors.Is(err, sendmuxJMAPError("stateMismatch")) {
			continue
		}
		if err != nil {
			return "", err
		}
		if len(created.NotCreated) > 0 || created.Created["send"].ID == "" {
			return "", errors.New("Sendmux did not accept the submission; reply remains pending")
		}
		return s.recordSent(ctx, from, session, emailID, key)
	}
	return "", errors.New("Sendmux submissions changed repeatedly; reply remains pending")
}

// A Sent copy proves submission, not delivery. Recover this separate, idempotent
// mailbox update after a lost response without creating another submission.
func (s *sendmuxJMAPSender) recordSent(ctx context.Context, from string, session sendmuxJMAPSession, emailID, key string) (string, error) {
	var result struct {
		Updated    map[string]json.RawMessage
		NotUpdated map[string]json.RawMessage
	}
	err := s.call(ctx, from, session, "Email/set", map[string]any{"update": map[string]any{emailID: map[string]any{"mailboxIds": map[string]bool{session.Sent: true}, "keywords": map[string]bool{"$seen": true, key: true}}}}, &result)
	if err != nil {
		return "", err
	}
	if _, ok := result.Updated[emailID]; !ok || len(result.NotUpdated) > 0 {
		return "", errors.New("Sendmux reply was submitted but its Sent receipt needs recovery")
	}
	return emailID, nil
}
