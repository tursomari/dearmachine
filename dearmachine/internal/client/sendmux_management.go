package client

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/mail"
	"os"
	"path/filepath"
	"strings"

	"sendmux.ai/go/core"
	"sendmux.ai/go/management"
)

const sendmuxSharedDomain = "myagent.mx"

type sendmuxManagementAPI interface {
	CreateMailbox(context.Context, string) (sendmuxProvisionedMailbox, error)
	DeleteMailbox(context.Context, string) error
	GetMailboxFilters(context.Context, string) (sendmuxFilterState, error)
	SetMailboxFilters(context.Context, string, sendmuxFilterState) error
}

type sendmuxProvisionedMailbox struct {
	ID         string
	Email      string
	Status     string
	Credential string
}

type sendmuxFilterRule struct {
	Type    string
	Pattern string
}

type sendmuxFilterState struct {
	Mode     string
	Rules    []sendmuxFilterRule
	Revision string
}

type sendmuxProvisionConfig struct {
	API          sendmuxManagementAPI
	UserHomeDir  func() (string, error)
	RandomSuffix func() (string, error)
}

type sendmuxSDKManagementAPI struct {
	client *management.Client
}

// ProvisionSendmuxInbox creates a mailbox with an Infrastructure key, then
// stores only the one-time mailbox credential for normal DearMachine runtime.
func ProvisionSendmuxInbox(ctx context.Context) (Inbox, error) {
	credential, configured, err := loadSendmuxManagementCredential()
	if err != nil {
		return Inbox{}, err
	}
	if !configured {
		return Inbox{}, fmt.Errorf("SENDMUX_API_KEY or SENDMUX_API_KEY_FILE is required to create a Sendmux inbox")
	}
	sdk, err := management.New(credential, management.WithRetryOptions(core.RetryOptions{MaxAttempts: 1}))
	if err != nil {
		return Inbox{}, fmt.Errorf("create Sendmux management client: %w", err)
	}
	return provisionSendmuxInbox(ctx, sendmuxProvisionConfig{
		API: &sendmuxSDKManagementAPI{client: sdk}, UserHomeDir: os.UserHomeDir, RandomSuffix: randomSendmuxSuffix,
	})
}

func provisionSendmuxInbox(ctx context.Context, config sendmuxProvisionConfig) (Inbox, error) {
	if config.API == nil || config.UserHomeDir == nil || config.RandomSuffix == nil {
		return Inbox{}, fmt.Errorf("Sendmux provisioning configuration is incomplete")
	}
	suffix, err := config.RandomSuffix()
	if err != nil {
		return Inbox{}, fmt.Errorf("generate Sendmux mailbox address: %w", err)
	}
	address := "dearmachine-" + strings.ToLower(strings.TrimSpace(suffix)) + "@" + sendmuxSharedDomain
	created, err := config.API.CreateMailbox(ctx, address)
	if err != nil {
		return Inbox{}, fmt.Errorf("create Sendmux mailbox: %w", err)
	}
	if strings.TrimSpace(created.ID) == "" || strings.TrimSpace(created.Email) == "" ||
		!strings.EqualFold(strings.TrimSpace(created.Status), "active") || strings.TrimSpace(created.Credential) == "" {
		_ = config.API.DeleteMailbox(ctx, created.ID)
		return Inbox{}, fmt.Errorf("create Sendmux mailbox: provider returned incomplete mailbox metadata or credential")
	}
	canonical, err := canonicalMessageAddress(created.Email)
	if err != nil {
		_ = config.API.DeleteMailbox(ctx, created.ID)
		return Inbox{}, fmt.Errorf("create Sendmux mailbox: invalid provider address: %w", err)
	}
	var storedPaths []string
	var pathErr error
	for _, selection := range []string{created.ID, canonical} {
		credentialPath, err := sendmuxStoredCredentialPath(config.UserHomeDir, selection)
		if err != nil {
			pathErr = err
			break
		}
		if err := os.MkdirAll(filepath.Dir(credentialPath), 0o700); err != nil {
			pathErr = err
			break
		}
		if err := os.Chmod(filepath.Dir(credentialPath), 0o700); err != nil {
			pathErr = err
			break
		}
		if err := writeAtomicConfig(credentialPath, []byte(strings.TrimSpace(created.Credential)+"\n")); err != nil {
			pathErr = err
			break
		}
		storedPaths = append(storedPaths, credentialPath)
	}
	if pathErr != nil {
		for _, path := range storedPaths {
			_ = os.Remove(path)
		}
		rollbackErr := config.API.DeleteMailbox(ctx, created.ID)
		if rollbackErr != nil {
			return Inbox{}, errors.Join(fmt.Errorf("store Sendmux mailbox credential: %w", pathErr), fmt.Errorf("rollback Sendmux mailbox: %w", rollbackErr))
		}
		return Inbox{}, fmt.Errorf("store Sendmux mailbox credential: %w", pathErr)
	}
	return Inbox{Transport: "sendmux", ProviderID: strings.TrimSpace(created.ID), Address: canonical}, nil
}

func randomSendmuxSuffix() (string, error) {
	buffer := make([]byte, 10)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func sendmuxStoredCredentialPath(userHomeDir func() (string, error), inboxID string) (string, error) {
	home, err := userHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve Sendmux credential home: %w", err)
	}
	if strings.TrimSpace(home) == "" || strings.TrimSpace(inboxID) == "" {
		return "", fmt.Errorf("Sendmux credential home and inbox ID are required")
	}
	digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(inboxID))))
	return filepath.Join(home, ".dearmachine", "credentials", "sendmux", hex.EncodeToString(digest[:])+".key"), nil
}

func loadSendmuxManagementCredential() (credential string, configured bool, err error) {
	if credential = strings.TrimSpace(os.Getenv("SENDMUX_API_KEY")); credential != "" {
		return credential, true, nil
	}
	credentialPath := strings.TrimSpace(os.Getenv("SENDMUX_API_KEY_FILE"))
	if credentialPath == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", false, nil
		}
		credentialPath = filepath.Join(home, ".config", "dearmachine", "sendmux-infrastructure-api-key")
		if _, statErr := os.Stat(credentialPath); errors.Is(statErr, os.ErrNotExist) {
			return "", false, nil
		}
	}
	contents, err := os.ReadFile(credentialPath)
	if err != nil {
		return "", true, fmt.Errorf("read SENDMUX_API_KEY_FILE: %w", err)
	}
	credential = strings.TrimRight(string(contents), "\r\n")
	if strings.TrimSpace(credential) == "" || strings.ContainsAny(credential, "\r\n") {
		return "", true, fmt.Errorf("SENDMUX_API_KEY_FILE must contain exactly one non-empty line")
	}
	return credential, true, nil
}

func AuthorizeSendmuxPair(ctx context.Context, inboxID, email string) error {
	credential, configured, err := loadSendmuxManagementCredential()
	if err != nil {
		return err
	}
	if !configured {
		return fmt.Errorf("SENDMUX_API_KEY or SENDMUX_API_KEY_FILE is required to authorize a Sendmux pair")
	}
	sdk, err := management.New(credential, management.WithRetryOptions(core.RetryOptions{MaxAttempts: 1}))
	if err != nil {
		return fmt.Errorf("create Sendmux management client: %w", err)
	}
	return authorizeSendmuxPair(ctx, &sendmuxSDKManagementAPI{client: sdk}, inboxID, email)
}

func authorizeSendmuxPair(ctx context.Context, api sendmuxManagementAPI, inboxID, email string) error {
	parsed, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || parsed.Address == "" {
		return fmt.Errorf("authorize Sendmux pair: must be an RFC 5322 address")
	}
	address := strings.ToLower(strings.TrimSpace(parsed.Address))
	state, err := api.GetMailboxFilters(ctx, inboxID)
	if err != nil {
		return fmt.Errorf("get Sendmux mailbox filters: %w", err)
	}
	present := false
	for _, rule := range state.Rules {
		if rule.Type == "allow" && strings.EqualFold(rule.Pattern, address) {
			present = true
			break
		}
	}
	if present && state.Mode == "allowlist" {
		return nil
	}
	if !present {
		state.Rules = append(state.Rules, sendmuxFilterRule{Type: "allow", Pattern: address})
	}
	state.Mode = "allowlist"
	if err := api.SetMailboxFilters(ctx, inboxID, state); err != nil {
		return fmt.Errorf("set Sendmux mailbox filters: %w", err)
	}
	return nil
}

func (api *sendmuxSDKManagementAPI) CreateMailbox(ctx context.Context, email string) (sendmuxProvisionedMailbox, error) {
	response, err := api.client.ManagementCreateMailbox(ctx, management.NewOptManagementCreateMailboxReq(
		management.ManagementCreateMailboxReq{Email: email},
	), management.ManagementCreateMailboxParams{})
	if err != nil {
		return sendmuxProvisionedMailbox{}, err
	}
	success, ok := response.(*management.MailboxCreateResultResponseHeaders)
	if !ok {
		return sendmuxProvisionedMailbox{}, sendmuxUnexpectedResponse("create mailbox", response)
	}
	responseBody := success.GetResponse()
	data := responseBody.GetData()
	mailbox := data.GetMailbox()
	credential, ok := data.GetCredential().Get()
	if !ok {
		keyResponse, keyErr := api.client.ManagementCreateMailboxKey(ctx,
			management.NewOptManagementCreateMailboxKeyReq(management.ManagementCreateMailboxKeyReq{AppName: "DearMachine"}),
			management.ManagementCreateMailboxKeyParams{PublicID: mailbox.GetID()},
		)
		if keyErr != nil {
			return api.rollbackCreatedMailbox(ctx, mailbox.GetID(), fmt.Errorf("mint Sendmux mailbox credential: %w", keyErr))
		}
		keySuccess, keyOK := keyResponse.(*management.MailboxAppPasswordResultResponseHeaders)
		if !keyOK {
			return api.rollbackCreatedMailbox(ctx, mailbox.GetID(), sendmuxUnexpectedResponse("mint mailbox credential", keyResponse))
		}
		keyResponseBody := keySuccess.GetResponse()
		keyData := keyResponseBody.GetData()
		keyCredential, credentialOK := keyData.GetCredential().Get()
		if !credentialOK {
			return api.rollbackCreatedMailbox(ctx, mailbox.GetID(), fmt.Errorf("mint Sendmux mailbox credential: provider returned no credential"))
		}
		return sendmuxProvisionedMailbox{
			ID: mailbox.GetID(), Email: mailbox.GetEmail(), Status: mailbox.GetStatus(), Credential: keyCredential.GetSecret(),
		}, nil
	}
	return sendmuxProvisionedMailbox{
		ID: mailbox.GetID(), Email: mailbox.GetEmail(), Status: mailbox.GetStatus(), Credential: credential.GetSecret(),
	}, nil
}

func (api *sendmuxSDKManagementAPI) rollbackCreatedMailbox(ctx context.Context, inboxID string, cause error) (sendmuxProvisionedMailbox, error) {
	if strings.TrimSpace(inboxID) == "" {
		return sendmuxProvisionedMailbox{}, cause
	}
	if err := api.DeleteMailbox(ctx, inboxID); err != nil {
		return sendmuxProvisionedMailbox{}, errors.Join(cause, fmt.Errorf("rollback Sendmux mailbox: %w", err))
	}
	return sendmuxProvisionedMailbox{}, cause
}

func (api *sendmuxSDKManagementAPI) DeleteMailbox(ctx context.Context, inboxID string) error {
	response, err := api.client.ManagementDeleteMailbox(ctx, management.ManagementDeleteMailboxParams{PublicID: inboxID})
	if err != nil {
		return err
	}
	if _, ok := response.(*management.MailboxDeletedResponse); !ok {
		return sendmuxUnexpectedResponse("delete mailbox", response)
	}
	return nil
}

func (api *sendmuxSDKManagementAPI) GetMailboxFilters(ctx context.Context, inboxID string) (sendmuxFilterState, error) {
	response, err := api.client.ManagementGetMailboxFilters(ctx, management.ManagementGetMailboxFiltersParams{PublicID: inboxID})
	if err != nil {
		return sendmuxFilterState{}, err
	}
	success, ok := response.(*management.FilterStateResponseHeaders)
	if !ok {
		return sendmuxFilterState{}, sendmuxUnexpectedResponse("get mailbox filters", response)
	}
	responseBody := success.GetResponse()
	data := responseBody.GetData()
	state := sendmuxFilterState{Mode: string(data.GetMode())}
	state.Revision, _ = success.GetETag().Get()
	for _, rule := range data.GetRules() {
		state.Rules = append(state.Rules, sendmuxFilterRule{Type: string(rule.GetType()), Pattern: rule.GetPattern()})
	}
	return state, nil
}

func (api *sendmuxSDKManagementAPI) SetMailboxFilters(ctx context.Context, inboxID string, state sendmuxFilterState) error {
	rules := make([]management.FilterRule, 0, len(state.Rules))
	for _, rule := range state.Rules {
		rules = append(rules, management.FilterRule{Type: management.FilterRuleType(rule.Type), Pattern: rule.Pattern})
	}
	params := management.ManagementSetMailboxFiltersParams{PublicID: inboxID}
	if state.Revision != "" {
		params.IfMatch = management.IfMatch(state.Revision)
	}
	response, err := api.client.ManagementSetMailboxFilters(ctx, management.NewOptSetFilterStateBody(management.SetFilterStateBody{
		Mode: management.SetFilterStateBodyMode(state.Mode), Rules: rules,
	}), params)
	if err != nil {
		return err
	}
	if _, ok := response.(*management.FilterStateResponseHeaders); !ok {
		return sendmuxUnexpectedResponse("set mailbox filters", response)
	}
	return nil
}
