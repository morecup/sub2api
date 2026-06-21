package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// CodexDesktopAPIService handles Codex Desktop App management API calls
// (invite friends, rate-limit reset credits) to chatgpt.com/backend-api.
type CodexDesktopAPIService struct {
	clientFactory PrivacyClientFactory
	proxyRepo     ProxyRepository
}

// NewCodexDesktopAPIService creates a new CodexDesktopAPIService.
func NewCodexDesktopAPIService(clientFactory PrivacyClientFactory, proxyRepo ProxyRepository) *CodexDesktopAPIService {
	return &CodexDesktopAPIService{
		clientFactory: clientFactory,
		proxyRepo:     proxyRepo,
	}
}

const (
	codexDesktopAPIBaseURL        = "https://chatgpt.com/backend-api"
	codexDesktopAPIReferralKey    = "codex_referral_persistent_invite"
	codexDesktopAPIAttachAuth     = "1"
	codexDesktopAPIIntegrityState = "1"
	codexDesktopAPILanguage       = "en"
)

// InviteEligibility represents the eligibility response for referral invite.
type InviteEligibility struct {
	IsEligible bool   `json:"is_eligible"`
	Reason     string `json:"reason,omitempty"`
	Raw        any    `json:"raw,omitempty"`
}

// ResetCredit represents a rate-limit reset credit entry.
type ResetCredit struct {
	CreditID        string `json:"credit_id"`
	RedeemRequestID string `json:"redeem_request_id,omitempty"`
	Raw             any    `json:"raw,omitempty"`
}

// InviteFriendsResult represents the result of inviting friends.
type InviteFriendsResult struct {
	Success bool `json:"success"`
	Raw     any  `json:"raw,omitempty"`
}

// ConsumeCreditResult represents the result of consuming a reset credit.
type ConsumeCreditResult struct {
	Success bool `json:"success"`
	Raw     any  `json:"raw,omitempty"`
}

func (s *CodexDesktopAPIService) setDesktopHeaders(req *http.Request, accessToken, chatgptAccountID string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("ChatGPT-Account-Id", chatgptAccountID)
	req.Header.Set("OAI-Language", codexDesktopAPILanguage)
	req.Header.Set("X-OpenAI-Attach-Auth", codexDesktopAPIAttachAuth)
	req.Header.Set("X-OpenAI-Attach-Integrity-State", codexDesktopAPIIntegrityState)
	req.Header.Set("originator", codexDesktopOriginator)
}

func (s *CodexDesktopAPIService) resolveProxyURL(ctx context.Context, account *Account) string {
	if account.ProxyID == nil {
		return ""
	}
	proxy, err := s.proxyRepo.GetByID(ctx, *account.ProxyID)
	if err != nil || proxy == nil {
		return ""
	}
	return proxy.URL()
}

func (s *CodexDesktopAPIService) extractAccountAuth(account *Account) (accessToken, chatgptAccountID string, err error) {
	accessToken = strings.TrimSpace(account.GetCredential("access_token"))
	if accessToken == "" {
		return "", "", infraerrors.New(http.StatusBadRequest, "CODEX_DESKTOP_NO_ACCESS_TOKEN", "account has no access_token")
	}
	chatgptAccountID = strings.TrimSpace(account.GetCredential("chatgpt_account_id"))
	if chatgptAccountID == "" {
		return "", "", infraerrors.New(http.StatusBadRequest, "CODEX_DESKTOP_NO_ACCOUNT_ID", "account has no chatgpt_account_id")
	}
	return accessToken, chatgptAccountID, nil
}

// GetInviteEligibility queries whether the current user is eligible to invite friends.
func (s *CodexDesktopAPIService) GetInviteEligibility(ctx context.Context, account *Account) (*InviteEligibility, error) {
	accessToken, chatgptAccountID, err := s.extractAccountAuth(account)
	if err != nil {
		return nil, err
	}
	proxyURL := s.resolveProxyURL(ctx, account)

	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_CLIENT_ERROR", "failed to create HTTP client: %v", err)
	}

	url := fmt.Sprintf("%s/referrals/invite/eligibility?referral_key=%s", codexDesktopAPIBaseURL, codexDesktopAPIReferralKey)

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("ChatGPT-Account-Id", chatgptAccountID).
		SetHeader("OAI-Language", codexDesktopAPILanguage).
		SetHeader("X-OpenAI-Attach-Auth", codexDesktopAPIAttachAuth).
		SetHeader("X-OpenAI-Attach-Integrity-State", codexDesktopAPIIntegrityState).
		SetHeader("originator", codexDesktopOriginator).
		SetHeader("Accept", "application/json").
		Get(url)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_REQUEST_FAILED", "request failed: %v", err)
	}
	if !resp.IsSuccessState() {
		return nil, infraerrors.Newf(resp.StatusCode, "CODEX_DESKTOP_ELIGIBILITY_FAILED", "eligibility check failed: status %d, body: %s", resp.StatusCode, truncate(resp.String(), 300))
	}

	var raw any
	if err := json.Unmarshal([]byte(resp.String()), &raw); err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "CODEX_DESKTOP_PARSE_ERROR", "failed to parse response: %v", err)
	}

	result := &InviteEligibility{Raw: raw}
	if m, ok := raw.(map[string]any); ok {
		if eligible, ok := m["is_eligible"].(bool); ok {
			result.IsEligible = eligible
		}
		if reason, ok := m["reason"].(string); ok {
			result.Reason = reason
		}
	}

	return result, nil
}

// InviteFriends sends invite emails to friends.
func (s *CodexDesktopAPIService) InviteFriends(ctx context.Context, account *Account, emails []string) (*InviteFriendsResult, error) {
	accessToken, chatgptAccountID, err := s.extractAccountAuth(account)
	if err != nil {
		return nil, err
	}
	if len(emails) == 0 {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_DESKTOP_NO_EMAILS", "at least one email is required")
	}
	proxyURL := s.resolveProxyURL(ctx, account)

	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_CLIENT_ERROR", "failed to create HTTP client: %v", err)
	}

	url := fmt.Sprintf("%s/wham/referrals/invite", codexDesktopAPIBaseURL)

	body := map[string]any{
		"referral_key": codexDesktopAPIReferralKey,
		"emails":       emails,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "CODEX_DESKTOP_MARSHAL_ERROR", "failed to marshal request body: %v", err)
	}

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("ChatGPT-Account-Id", chatgptAccountID).
		SetHeader("OAI-Language", codexDesktopAPILanguage).
		SetHeader("X-OpenAI-Attach-Auth", codexDesktopAPIAttachAuth).
		SetHeader("X-OpenAI-Attach-Integrity-State", codexDesktopAPIIntegrityState).
		SetHeader("originator", codexDesktopOriginator).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "application/json").
		SetBody(bodyBytes).
		Post(url)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_REQUEST_FAILED", "request failed: %v", err)
	}
	if !resp.IsSuccessState() {
		return nil, infraerrors.Newf(resp.StatusCode, "CODEX_DESKTOP_INVITE_FAILED", "invite failed: status %d, body: %s", resp.StatusCode, truncate(resp.String(), 300))
	}

	var raw any
	_ = json.Unmarshal([]byte(resp.String()), &raw)
	return &InviteFriendsResult{Success: true, Raw: raw}, nil
}

// GetRateLimitResetCredits fetches available rate-limit reset credits.
func (s *CodexDesktopAPIService) GetRateLimitResetCredits(ctx context.Context, account *Account) ([]ResetCredit, error) {
	accessToken, chatgptAccountID, err := s.extractAccountAuth(account)
	if err != nil {
		return nil, err
	}
	proxyURL := s.resolveProxyURL(ctx, account)

	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_CLIENT_ERROR", "failed to create HTTP client: %v", err)
	}

	url := fmt.Sprintf("%s/wham/rate-limit-reset-credits", codexDesktopAPIBaseURL)

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("ChatGPT-Account-Id", chatgptAccountID).
		SetHeader("OAI-Language", codexDesktopAPILanguage).
		SetHeader("X-OpenAI-Attach-Auth", codexDesktopAPIAttachAuth).
		SetHeader("X-OpenAI-Attach-Integrity-State", codexDesktopAPIIntegrityState).
		SetHeader("originator", codexDesktopOriginator).
		SetHeader("Accept", "application/json").
		Get(url)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_REQUEST_FAILED", "request failed: %v", err)
	}
	if !resp.IsSuccessState() {
		return nil, infraerrors.Newf(resp.StatusCode, "CODEX_DESKTOP_CREDITS_FAILED", "get credits failed: status %d, body: %s", resp.StatusCode, truncate(resp.String(), 300))
	}

	var raw any
	if err := json.Unmarshal([]byte(resp.String()), &raw); err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "CODEX_DESKTOP_PARSE_ERROR", "failed to parse response: %v", err)
	}

	credits := parseResetCredits(raw)
	return credits, nil
}

// ConsumeRateLimitResetCredit consumes a rate-limit reset credit.
func (s *CodexDesktopAPIService) ConsumeRateLimitResetCredit(ctx context.Context, account *Account, creditID, redeemRequestID string) (*ConsumeCreditResult, error) {
	accessToken, chatgptAccountID, err := s.extractAccountAuth(account)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(creditID) == "" {
		return nil, infraerrors.New(http.StatusBadRequest, "CODEX_DESKTOP_NO_CREDIT_ID", "credit_id is required")
	}
	proxyURL := s.resolveProxyURL(ctx, account)

	client, err := s.clientFactory(proxyURL)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_CLIENT_ERROR", "failed to create HTTP client: %v", err)
	}

	url := fmt.Sprintf("%s/wham/rate-limit-reset-credits/consume", codexDesktopAPIBaseURL)

	body := map[string]any{
		"credit_id":         creditID,
		"redeem_request_id": redeemRequestID,
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "CODEX_DESKTOP_MARSHAL_ERROR", "failed to marshal request body: %v", err)
	}

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("ChatGPT-Account-Id", chatgptAccountID).
		SetHeader("OAI-Language", codexDesktopAPILanguage).
		SetHeader("X-OpenAI-Attach-Auth", codexDesktopAPIAttachAuth).
		SetHeader("X-OpenAI-Attach-Integrity-State", codexDesktopAPIIntegrityState).
		SetHeader("originator", codexDesktopOriginator).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "application/json").
		SetBody(bodyBytes).
		Post(url)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_REQUEST_FAILED", "request failed: %v", err)
	}
	if !resp.IsSuccessState() {
		return nil, infraerrors.Newf(resp.StatusCode, "CODEX_DESKTOP_CONSUME_FAILED", "consume credit failed: status %d, body: %s", resp.StatusCode, truncate(resp.String(), 300))
	}

	var raw any
	_ = json.Unmarshal([]byte(resp.String()), &raw)
	return &ConsumeCreditResult{Success: true, Raw: raw}, nil
}

// parseResetCredits extracts ResetCredit entries from the API response.
func parseResetCredits(raw any) []ResetCredit {
	if raw == nil {
		return nil
	}

	// Response could be an array directly or an object with a "credits" field
	var items []any

	switch v := raw.(type) {
	case []any:
		items = v
	case map[string]any:
		if credits, ok := v["credits"].([]any); ok {
			items = credits
		} else if data, ok := v["data"].([]any); ok {
			items = data
		} else {
			// Single object — treat as one credit
			items = []any{v}
		}
	default:
		slog.Debug("codex_desktop_credits_unexpected_type", "type", fmt.Sprintf("%T", raw))
		return nil
	}

	credits := make([]ResetCredit, 0, len(items))
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		creditID, _ := m["credit_id"].(string)
		if creditID == "" {
			creditID, _ = m["id"].(string)
		}
		redeemRequestID, _ := m["redeem_request_id"].(string)
		if creditID == "" {
			continue
		}
		credits = append(credits, ResetCredit{
			CreditID:        creditID,
			RedeemRequestID: redeemRequestID,
			Raw:             m,
		})
	}
	return credits
}
