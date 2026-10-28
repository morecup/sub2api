package service

// Opt-in, administrator-only diagnostics for a real outbound request. The
// snapshot is written to a private temporary file, never to normal access logs.
// Credentials, proxy URLs, IDs from upstream metadata and prompt text are omitted.
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/klauspost/compress/zstd"
	"github.com/tidwall/gjson"
)

type codexRequestAuditKey struct{}

func withCodexRequestAudit(ctx context.Context, c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return ctx
	}
	value, ok := c.Get("api_key")
	key, valid := value.(*APIKey)
	if !ok || !valid || key == nil || key.User == nil || !key.User.IsAdmin() {
		return ctx
	}
	id, err := uuid.Parse(c.GetHeader("X-Sub2API-Codex-Audit"))
	if err != nil || id == uuid.Nil {
		return ctx
	}
	return context.WithValue(ctx, codexRequestAuditKey{}, id.String())
}

type codexRequestAuditSnapshot struct {
	AuditID                    string                         `json:"audit_id"`
	At                         string                         `json:"at"`
	AccountID                  int64                          `json:"account_id"`
	Model                      string                         `json:"model"`
	Transport                  string                         `json:"transport"`
	Status                     int                            `json:"status"`
	Sent                       bool                           `json:"sent"`
	ProxyEnabled               bool                           `json:"proxy_enabled"`
	EgressResolved             bool                           `json:"egress_resolved"`
	Locale                     string                         `json:"locale"`
	Timezone                   string                         `json:"timezone"`
	Headers                    map[string]string              `json:"headers"`
	HeaderNames                []string                       `json:"header_names"`
	Profile                    string                         `json:"tls_profile"`
	HeaderOrder                []string                       `json:"header_order"`
	AttestationTimezoneMatches bool                           `json:"attestation_timezone_matches"`
	AttestationLocaleMatches   bool                           `json:"attestation_locale_matches"`
	MetadataBodyMatches        bool                           `json:"metadata_body_matches"`
	SessionCacheMatches        bool                           `json:"session_cache_matches"`
	WSMetadataAbsent           bool                           `json:"ws_metadata_absent"`
	Contexts                   []codexRequestAuditTimeContext `json:"time_contexts"`
}

type codexRequestAuditTimeContext struct {
	Role     string `json:"role"`
	Tag      string `json:"tag"`
	Timezone string `json:"timezone"`
	Date     string `json:"date"`
}

func buildCodexRequestAuditSnapshot(req *http.Request, body []byte, account *Account, profile *tlsfingerprint.Profile) codexRequestAuditSnapshot {
	client := codexClientProfileForAccount(account)
	snapshot := codexRequestAuditSnapshot{At: time.Now().UTC().Format(time.RFC3339Nano), AccountID: account.ID,
		Transport: "HTTP", Model: gjson.GetBytes(body, "model").String(), Locale: client.Locale, Timezone: client.Timezone,
		Headers: make(map[string]string), EgressResolved: account.codexEgressProfile != nil && account.codexEgressProfile.timezone != ""}
	for name := range req.Header {
		snapshot.HeaderNames = append(snapshot.HeaderNames, strings.ToLower(name))
	}
	sort.Strings(snapshot.HeaderNames)
	for _, name := range []string{"user-agent", "version", "originator", "accept", "content-type", "content-encoding", "openai-beta", "x-codex-beta-features", "x-openai-internal-codex-responses-lite"} {
		if value := req.Header.Get(name); value != "" {
			snapshot.Headers[name] = value
		}
	}
	if profile != nil {
		snapshot.Profile = profile.Name
		if profile.HTTP2 != nil {
			snapshot.HeaderOrder = profile.HTTP2.RegularHeaderOrder
		}
	}
	// Match the encoded CBOR text entries, rather than exposing the attestation.
	token := gjson.Get(req.Header.Get("x-oai-attestation"), "t").String()
	attestation, _ := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(token, "v1."))
	snapshot.AttestationTimezoneMatches = strings.Contains(string(attestation), string(appendCodexCBORText(nil, client.Timezone)))
	snapshot.AttestationLocaleMatches = strings.Contains(string(attestation), string(appendCodexCBORText(nil, client.Locale)))
	metadata := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata").String()
	snapshot.MetadataBodyMatches = metadata != "" && metadata == req.Header.Get(openAIWSTurnMetadataHeader)
	snapshot.SessionCacheMatches = req.Header.Get("session-id") != "" && req.Header.Get("session-id") == gjson.GetBytes(body, "prompt_cache_key").String()
	snapshot.WSMetadataAbsent = !gjson.GetBytes(body, "client_metadata."+responsesLiteWSMetadataKey).Exists() && !gjson.GetBytes(body, "client_metadata.x-codex-ws-stream-request-start-ms").Exists()
	for _, item := range gjson.GetBytes(body, "input").Array() {
		role := item.Get("role").String()
		if role != "user" && role != "developer" {
			continue
		}
		for _, part := range item.Get("content").Array() {
			text := strings.TrimSpace(part.Get("text").String())
			for _, tag := range []string{"environment_context", "codex_apps_client_time_context"} {
				if !strings.HasPrefix(text, "<"+tag+">") || !strings.HasSuffix(text, "</"+tag+">") {
					continue
				}
				zone := codexAuditTag(text, "timezone")
				date := codexAuditTag(text, "current_date")
				// Do not expose arbitrary text supplied inside a tag.
				if _, err := time.LoadLocation(zone); err != nil || len(zone) > 128 {
					zone = "invalid"
				}
				if _, err := time.Parse("2006-01-02", date); err != nil {
					date = "invalid"
				}
				snapshot.Contexts = append(snapshot.Contexts, codexRequestAuditTimeContext{role, tag, zone, date})
				if len(snapshot.Contexts) >= 16 {
					return snapshot
				}
			}
		}
	}
	return snapshot
}

func codexAuditTag(text, tag string) string {
	_, value, ok := strings.Cut(text, "<"+tag+">")
	if !ok {
		return ""
	}
	value, _, _ = strings.Cut(value, "</"+tag+">")
	return value
}

func observeCodexRequestAudit(req *http.Request, resp *http.Response, account *Account, proxyURL string, profile *tlsfingerprint.Profile, sendErr error) {
	id, ok := req.Context().Value(codexRequestAuditKey{}).(string)
	if !ok || account == nil || !account.IsOpenAIOAuth() || req.GetBody == nil {
		return
	}
	reader, err := req.GetBody()
	if err != nil {
		return
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		return
	}
	if req.Header.Get("content-encoding") == "zstd" {
		decoder, err := zstd.NewReader(nil, zstd.WithDecoderMaxMemory(32<<20), zstd.WithDecodeAllCapLimit(true))
		if err != nil {
			return
		}
		defer decoder.Close()
		body, err = decoder.DecodeAll(body, make([]byte, 0, 4<<20))
		if err != nil || len(body) > 4<<20 {
			return
		}
	}
	snapshot := buildCodexRequestAuditSnapshot(req, body, account, profile)
	snapshot.AuditID = id
	snapshot.Sent = sendErr == nil
	snapshot.ProxyEnabled = proxyURL != ""
	if resp != nil {
		snapshot.Status = resp.StatusCode
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return
	}
	file, err := os.CreateTemp("", "sub2api-codex-audit-"+id+"-*.json")
	if err != nil {
		return
	}
	defer file.Close()
	_, _ = file.Write(data)
}
