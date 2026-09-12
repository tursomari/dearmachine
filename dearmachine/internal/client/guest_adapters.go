package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	agentmail "github.com/agentmail-to/agentmail-go"
	"github.com/agentmail-to/agentmail-go/option"
	"sendmux.ai/go/core"
	"sendmux.ai/go/management"
)

func (m *Mailbox) InspectReceive(ctx context.Context, address string) (ReceivePermission, error) {
	if err := exactReceiveAddress(address); err != nil {
		return ReceivePermission{}, err
	}
	entry, err := m.client.Inboxes.Lists.Get(ctx, address, agentmail.InboxListGetParams{InboxID: m.inboxID, Direction: agentmail.InboxListGetParamsDirectionReceive, Type: agentmail.InboxListGetParamsTypeAllow})
	if agentMailStatus(err, http.StatusNotFound) {
		return ReceivePermission{}, nil
	}
	if err != nil {
		return ReceivePermission{}, err
	}
	if entry.Entry != address || entry.Direction != "receive" || entry.ListType != "allow" || entry.EntryType != "email" {
		return ReceivePermission{}, errors.New("AgentMail returned a different receive rule")
	}
	token := ""
	if entry.InboxID == m.inboxID && !entry.ReadOnly && !entry.CreatedAt.IsZero() {
		token = entry.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return ReceivePermission{true, token}, nil
}
func (m *Mailbox) AddReceive(ctx context.Context, address string) (ReceivePermission, error) {
	if err := exactReceiveAddress(address); err != nil {
		return ReceivePermission{}, err
	}
	entry, err := m.client.Inboxes.Lists.New(ctx, agentmail.InboxListNewParamsTypeAllow, agentmail.InboxListNewParams{InboxID: m.inboxID, Direction: agentmail.InboxListNewParamsDirectionReceive, Entry: address}, option.WithMaxRetries(0))
	if agentMailStatus(err, http.StatusConflict) {
		observed, inspectErr := m.InspectReceive(ctx, address)
		observed.Token = ""
		return observed, inspectErr
	}
	if err != nil {
		return ReceivePermission{}, err
	}
	if entry.Entry != address || entry.Direction != "receive" || entry.ListType != "allow" || entry.EntryType != "email" || entry.InboxID != m.inboxID {
		return ReceivePermission{}, errors.New("AgentMail did not confirm the exact receive entry")
	}
	token := ""
	if !entry.CreatedAt.IsZero() && !entry.ReadOnly {
		token = entry.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	return ReceivePermission{true, token}, nil
}
func (m *Mailbox) RemoveReceive(ctx context.Context, address, token string) error {
	observed, err := m.InspectReceive(ctx, address)
	if err != nil {
		return err
	}
	if !observed.Present || token == "" || token != observed.Token {
		return nil
	}
	err = m.client.Inboxes.Lists.Delete(ctx, address, agentmail.InboxListDeleteParams{InboxID: m.inboxID, Direction: agentmail.InboxListDeleteParamsDirectionReceive, Type: agentmail.InboxListDeleteParamsTypeAllow}, option.WithMaxRetries(0))
	if agentMailStatus(err, http.StatusNotFound) {
		return nil
	}
	return err
}

func exactReceiveAddress(address string) error {
	canonical, err := canonicalMessageAddress(address)
	if err != nil || canonical != address {
		return errors.New("provider receive permission requires a canonical exact mailbox address")
	}
	return nil
}

type openMailReceiveRule struct {
	ID        string `json:"id"`
	Direction string `json:"direction"`
	Type      string `json:"type"`
	Value     string `json:"value"`
}

func (m *OpenMailTransport) InspectReceive(ctx context.Context, address string) (ReceivePermission, error) {
	if err := exactReceiveAddress(address); err != nil {
		return ReceivePermission{}, err
	}
	inbox, err := m.inboxID(ctx)
	if err != nil {
		return ReceivePermission{}, err
	}
	var policy struct {
		Rules *[]openMailReceiveRule `json:"rules"`
	}
	if err = m.getJSON(ctx, "/v1/policy?inboxId="+url.QueryEscape(inbox), &policy); err != nil {
		return ReceivePermission{}, err
	}
	if policy.Rules == nil {
		return ReceivePermission{}, errors.New("OpenMail policy response omitted scoped rules")
	}
	for _, rule := range *policy.Rules {
		if rule.Direction == "inbound" && rule.Type == "allow" && rule.Value == address {
			return ReceivePermission{true, rule.ID}, nil
		}
	}
	return ReceivePermission{}, nil
}
func (m *OpenMailTransport) AddReceive(ctx context.Context, address string) (ReceivePermission, error) {
	if err := exactReceiveAddress(address); err != nil {
		return ReceivePermission{}, err
	}
	inbox, err := m.inboxID(ctx)
	if err != nil {
		return ReceivePermission{}, err
	}
	var created struct {
		ID string `json:"id"`
	}
	err = m.mutateJSON(ctx, http.MethodPost, "/v1/policy/rules?inboxId="+url.QueryEscape(inbox), openMailPolicyRule{Type: "allow", Value: address, Direction: "inbound"}, &created)
	if err != nil {
		var apiErr *openMailAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusConflict && apiErr.Code == "rule_exists" {
			observed, inspectErr := m.InspectReceive(ctx, address)
			observed.Token = ""
			return observed, inspectErr
		}
		return ReceivePermission{}, err
	}
	observed, err := m.InspectReceive(ctx, address)
	if err != nil {
		return ReceivePermission{}, err
	}
	// A missing ID in the successful create response establishes no ownership.
	if created.ID == "" || observed.Token != created.ID {
		observed.Token = ""
	}
	return observed, nil
}
func (m *OpenMailTransport) RemoveReceive(ctx context.Context, address, token string) error {
	observed, err := m.InspectReceive(ctx, address)
	if err != nil {
		return err
	}
	if !observed.Present || token == "" || observed.Token != token {
		return nil
	}
	inbox, err := m.inboxID(ctx)
	if err != nil {
		return err
	}
	req, err := m.request(ctx, http.MethodDelete, "/v1/policy/rules/"+url.PathEscape(token)+"?inboxId="+url.QueryEscape(inbox), nil)
	if err != nil {
		return err
	}
	err = m.doJSON(req, nil)
	var apiErr *openMailAPIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
		return nil
	}
	return err
}

type sendmuxReceiveAuthorizer struct {
	api     sendmuxManagementAPI
	inboxID string
}

func (p *sendmuxReceiveAuthorizer) InspectReceive(ctx context.Context, address string) (ReceivePermission, error) {
	if err := exactReceiveAddress(address); err != nil {
		return ReceivePermission{}, err
	}
	state, err := p.api.GetMailboxFilters(ctx, p.inboxID)
	if err != nil {
		return ReceivePermission{}, err
	}
	for _, rule := range state.Rules {
		if rule.Type == "allow" && rule.Pattern == address {
			return ReceivePermission{true, state.Revision}, nil
		}
	}
	return ReceivePermission{}, nil
}
func (p *sendmuxReceiveAuthorizer) AddReceive(ctx context.Context, address string) (ReceivePermission, error) {
	if err := exactReceiveAddress(address); err != nil {
		return ReceivePermission{}, err
	}
	state, err := p.api.GetMailboxFilters(ctx, p.inboxID)
	if err != nil {
		return ReceivePermission{}, err
	}
	for _, rule := range state.Rules {
		if rule.Type == "allow" && rule.Pattern == address {
			return ReceivePermission{Present: true}, nil
		}
	}
	if state.Revision == "" {
		return ReceivePermission{}, errors.New("Sendmux omitted filter ETag; cannot safely mutate filters")
	}
	state.Rules = append(state.Rules, sendmuxFilterRule{Type: "allow", Pattern: address})
	revision := ""
	if observedAPI, ok := p.api.(interface {
		SetMailboxFiltersWithRevision(context.Context, string, sendmuxFilterState) (string, error)
	}); ok {
		revision, err = observedAPI.SetMailboxFiltersWithRevision(ctx, p.inboxID, state)
	} else {
		err = p.api.SetMailboxFilters(ctx, p.inboxID, state)
	}
	if err != nil {
		return ReceivePermission{}, err
	}
	observed, err := p.InspectReceive(ctx, address)
	if observed.Token != revision {
		observed.Token = ""
	}
	return observed, err
}
func (p *sendmuxReceiveAuthorizer) RemoveReceive(ctx context.Context, address, token string) error {
	if err := exactReceiveAddress(address); err != nil {
		return err
	}
	state, err := p.api.GetMailboxFilters(ctx, p.inboxID)
	if err != nil {
		return err
	}
	if token == "" || state.Revision != token {
		return nil
	}
	rules := make([]sendmuxFilterRule, 0, len(state.Rules))
	found := false
	for _, rule := range state.Rules {
		if rule.Type == "allow" && rule.Pattern == address {
			found = true
			continue
		}
		rules = append(rules, rule)
	}
	if !found {
		return nil
	}
	state.Rules = rules
	return p.api.SetMailboxFilters(ctx, p.inboxID, state)
}
func (m *SendmuxTransport) receiveAuthorizer(ctx context.Context) (*sendmuxReceiveAuthorizer, error) {
	credential, configured, err := loadSendmuxManagementCredential()
	if err != nil {
		return nil, err
	}
	if !configured {
		return nil, errors.New("Sendmux receive synchronization requires infrastructure credentials")
	}
	sdk, err := management.New(credential, management.WithRetryOptions(core.RetryOptions{MaxAttempts: 1}))
	if err != nil {
		return nil, err
	}
	resolved, err := m.mailbox(ctx)
	if err != nil {
		return nil, err
	}
	return &sendmuxReceiveAuthorizer{api: &sendmuxSDKManagementAPI{client: sdk}, inboxID: resolved.ID}, nil
}
func (m *SendmuxTransport) InspectReceive(ctx context.Context, address string) (ReceivePermission, error) {
	p, err := m.receiveAuthorizer(ctx)
	if err != nil {
		return ReceivePermission{}, err
	}
	return p.InspectReceive(ctx, address)
}
func (m *SendmuxTransport) AddReceive(ctx context.Context, address string) (ReceivePermission, error) {
	p, err := m.receiveAuthorizer(ctx)
	if err != nil {
		return ReceivePermission{}, err
	}
	return p.AddReceive(ctx, address)
}
func (m *SendmuxTransport) RemoveReceive(ctx context.Context, address, token string) error {
	p, err := m.receiveAuthorizer(ctx)
	if err != nil {
		return err
	}
	return p.RemoveReceive(ctx, address, token)
}

func (t *retryTransport) AuthenticatedSender(ctx context.Context, id string) (string, error) {
	if p, ok := t.Transport.(ControllerAttributor); ok {
		return p.AuthenticatedSender(ctx, id)
	}
	return "", ErrSenderAttributionUnsupported
}
func (t *retryTransport) InspectReceive(ctx context.Context, address string) (ReceivePermission, error) {
	if p, ok := t.Transport.(ReceiveAuthorizer); ok {
		return p.InspectReceive(ctx, address)
	}
	return ReceivePermission{}, fmt.Errorf("transport %T does not support receive synchronization", t.Transport)
}
func (t *retryTransport) AddReceive(ctx context.Context, address string) (ReceivePermission, error) {
	if p, ok := t.Transport.(ReceiveAuthorizer); ok {
		return p.AddReceive(ctx, address)
	}
	return ReceivePermission{}, errors.New("receive synchronization unsupported")
}
func (t *retryTransport) RemoveReceive(ctx context.Context, address, token string) error {
	if p, ok := t.Transport.(ReceiveAuthorizer); ok {
		return p.RemoveReceive(ctx, address, token)
	}
	return errors.New("receive synchronization unsupported")
}
