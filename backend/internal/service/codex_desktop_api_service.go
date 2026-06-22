package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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
	IsEligible           bool   `json:"is_eligible"`
	ShouldShow           *bool  `json:"should_show,omitempty"`
	GrantAction          string `json:"grant_action,omitempty"`
	GrantAmount          *int64 `json:"grant_amount,omitempty"`
	RemainingReferrals   *int64 `json:"remaining_referrals,omitempty"`
	IneligibleReason     string `json:"ineligible_reason,omitempty"`
	IneligibleReasonCode string `json:"ineligible_reason_code,omitempty"`
	Reason               string `json:"reason,omitempty"`
	Raw                  any    `json:"raw,omitempty"`
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
	req.Header.Set("User-Agent", codexDesktopUserAgent)
	req.Header.Set("OAI-Language", codexDesktopAPILanguage)
	req.Header.Set("X-OpenAI-Attach-Auth", codexDesktopAPIAttachAuth)
	req.Header.Set("X-OpenAI-Attach-Integrity-State", codexDesktopAPIIntegrityState)
	req.Header.Set("originator", codexDesktopOriginator)
}

func codexDesktopOptionalCookie(account *Account) string {
	if account == nil {
		return ""
	}
	for _, key := range []string{"chatgpt_cookie", "chatgpt_browser_cookie", "browser_cookie", "cookie"} {
		if value := strings.TrimSpace(account.GetCredential(key)); value != "" {
			return value
		}
	}
	return ""
}

func codexDesktopOptionalBrowserUserAgent(account *Account) string {
	if account == nil {
		return codexDesktopUserAgent
	}
	for _, key := range []string{"chatgpt_user_agent", "chatgpt_browser_user_agent", "browser_user_agent"} {
		if value := strings.TrimSpace(account.GetCredential(key)); value != "" {
			return value
		}
	}
	return codexDesktopUserAgent
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

	request := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("ChatGPT-Account-Id", chatgptAccountID).
		SetHeader("User-Agent", codexDesktopOptionalBrowserUserAgent(account)).
		SetHeader("OAI-Language", codexDesktopAPILanguage).
		SetHeader("X-OpenAI-Attach-Auth", codexDesktopAPIAttachAuth).
		SetHeader("X-OpenAI-Attach-Integrity-State", codexDesktopAPIIntegrityState).
		SetHeader("originator", codexDesktopOriginator).
		SetHeader("Accept", "application/json")
	if cookie := codexDesktopOptionalCookie(account); cookie != "" {
		request.SetHeader("Cookie", cookie)
	}
	resp, err := request.Get(url)
	if err != nil {
		return nil, infraerrors.Newf(http.StatusBadGateway, "CODEX_DESKTOP_REQUEST_FAILED", "request failed: %v", err)
	}
	if !resp.IsSuccessState() {
		if resp.StatusCode == http.StatusForbidden {
			return nil, infraerrors.Newf(resp.StatusCode, "CODEX_DESKTOP_ELIGIBILITY_FORBIDDEN", "eligibility check forbidden: upstream may require the same-account ChatGPT browser Cookie; status %d, body: %s", resp.StatusCode, truncate(resp.String(), 300))
		}
		return nil, infraerrors.Newf(resp.StatusCode, "CODEX_DESKTOP_ELIGIBILITY_FAILED", "eligibility check failed: status %d, body: %s", resp.StatusCode, truncate(resp.String(), 300))
	}

	var raw any
	if err := json.Unmarshal([]byte(resp.String()), &raw); err != nil {
		return nil, infraerrors.Newf(http.StatusInternalServerError, "CODEX_DESKTOP_PARSE_ERROR", "failed to parse response: %v", err)
	}

	return parseInviteEligibility(raw), nil
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

	request := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("ChatGPT-Account-Id", chatgptAccountID).
		SetHeader("User-Agent", codexDesktopOptionalBrowserUserAgent(account)).
		SetHeader("OAI-Language", codexDesktopAPILanguage).
		SetHeader("X-OpenAI-Attach-Auth", codexDesktopAPIAttachAuth).
		SetHeader("X-OpenAI-Attach-Integrity-State", codexDesktopAPIIntegrityState).
		SetHeader("originator", codexDesktopOriginator).
		SetHeader("Content-Type", "application/json").
		SetHeader("Accept", "application/json").
		SetBody(bodyBytes)
	if cookie := codexDesktopOptionalCookie(account); cookie != "" {
		request.SetHeader("Cookie", cookie)
	}
	resp, err := request.Post(url)
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
		SetHeader("User-Agent", codexDesktopUserAgent).
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
		SetHeader("User-Agent", codexDesktopUserAgent).
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

func parseInviteEligibility(raw any) *InviteEligibility {
	result := &InviteEligibility{Raw: raw}
	m, ok := raw.(map[string]any)
	if !ok {
		return result
	}

	if shouldShow, ok := boolMapValue(m, "should_show"); ok {
		result.ShouldShow = &shouldShow
		result.IsEligible = shouldShow
	} else if eligible, ok := boolMapValue(m, "is_eligible"); ok {
		result.IsEligible = eligible
	} else if eligible, ok := boolMapValue(m, "eligible"); ok {
		result.IsEligible = eligible
	}

	result.GrantAction = stringMapValue(m, "grant_action")
	result.GrantAmount = int64MapValue(m, "grant_amount")
	result.RemainingReferrals = int64MapValue(m, "remaining_referrals")
	result.IneligibleReason = stringMapValue(m, "ineligible_reason")
	result.IneligibleReasonCode = stringMapValue(m, "ineligible_reason_code")

	result.Reason = firstNonEmptyInviteString(
		stringMapValue(m, "reason"),
		result.IneligibleReason,
		result.IneligibleReasonCode,
	)
	return result
}

func boolMapValue(m map[string]any, key string) (bool, bool) {
	value, ok := m[key]
	if !ok || value == nil {
		return false, false
	}
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(v))
		if err == nil {
			return parsed, true
		}
	}
	return false, false
}

func stringMapValue(m map[string]any, key string) string {
	value, ok := m[key]
	if !ok || value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func int64MapValue(m map[string]any, key string) *int64 {
	value, ok := m[key]
	if !ok || value == nil {
		return nil
	}
	var parsed int64
	switch v := value.(type) {
	case int:
		parsed = int64(v)
	case int64:
		parsed = v
	case float64:
		parsed = int64(v)
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return nil
		}
		parsed = n
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err != nil {
			return nil
		}
		parsed = n
	default:
		return nil
	}
	return &parsed
}

func firstNonEmptyInviteString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
