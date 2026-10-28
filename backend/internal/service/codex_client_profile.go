package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
)

const CodexClientProfileExtraKey = "openai_codex_client_profile"

// CodexClientProfile is a persisted compatibility environment, not a claim
// that the gateway runs on this hardware. IDs belong to one account; templates
// may be reused. Keep snapshots independent of future edits to the pool.
type CodexClientProfile struct {
	SchemaVersion  int     `json:"schema_version"`
	TemplateID     string  `json:"template_id"`
	InstallationID string  `json:"installation_id"`
	DeviceID       string  `json:"device_id"`
	CodexVersion   string  `json:"codex_version"`
	AppVersion     string  `json:"app_version"`
	OS             string  `json:"os"`
	OSVersion      string  `json:"os_version"`
	Arch           string  `json:"arch"`
	Locale         string  `json:"locale"`
	Timezone       string  `json:"timezone"`
	ScreenSizeSum  uint64  `json:"screen_size_sum"`
	ScreenScale    float64 `json:"screen_scale"`
	BrowserVersion string  `json:"browser_version"`
	OTelSDKVersion string  `json:"otel_sdk_version"`
}

func defaultCodexClientProfile() CodexClientProfile {
	return CodexClientProfile{
		SchemaVersion: 1, TemplateID: "capture-20261028", CodexVersion: codexDesktopVersion,
		AppVersion: codexDesktopAppVersion, OS: "Windows", OSVersion: "10.0.26100", Arch: "x86_64",
		Locale: codexAttestationLanguage, Timezone: codexAttestationTimezone,
		ScreenSizeSum: codexAttestationScreenSizeSum, ScreenScale: codexAttestationScreenScale,
		BrowserVersion: "156.0.0.0", OTelSDKVersion: codexTelemetrySDKVersion,
	}
}

// Sixteen complete Windows environments: two released OS builds, four
// display configurations and two UI languages. Versions of Codex/Electron/
// Chromium/OTel stay paired with the captured release. This order is also used
// by migration 193; v2/v3/v4 upgrade release versions (migrations 240/241/242). Do not
// reorder the environment slots when adding future releases.
func codexClientEnvironmentPool() []CodexClientProfile {
	displays := []struct {
		sum   uint64
		scale float64
	}{{3000, 1}, {4000, 1}, {4000, 1.25}, {6000, 1.5}}
	pool := make([]CodexClientProfile, 0, 16)
	for _, build := range []string{"10.0.26100", "10.0.26200"} {
		for _, display := range displays {
			for _, locale := range []string{"zh-CN", "en-US"} {
				p := defaultCodexClientProfile()
				p.TemplateID = fmt.Sprintf("windows-v4-%02d", len(pool))
				p.OSVersion, p.Locale = build, locale
				p.ScreenSizeSum, p.ScreenScale = display.sum, display.scale
				pool = append(pool, p)
			}
		}
	}
	return pool
}

func newCodexClientProfile(account *Account) CodexClientProfile {
	installationID, deviceID := uuid.NewString(), uuid.NewString()
	if account != nil && account.ID > 0 {
		// Backfill must not rotate an installation that has already made requests.
		installationID = codexInstallationIDForAccount(account.ID, account.GetChatGPTAccountID())
		deviceID = codexUUIDv4FromSeed("sub2api:codex-device:" + codexAccountSeed(account.ID, ""))
	}
	seed := sha256.Sum256([]byte(installationID))
	pool := codexClientEnvironmentPool()
	p := pool[int(seed[0])%len(pool)]
	p.InstallationID, p.DeviceID = installationID, deviceID
	return p
}

func (p CodexClientProfile) UserAgent() string {
	return p.NativeUserAgent() + " (Codex Desktop; " + p.AppVersion + ")"
}

func (p CodexClientProfile) NativeUserAgent() string {
	return fmt.Sprintf("Codex Desktop/%s (%s %s; %s) dumb", p.CodexVersion, p.OS, p.OSVersion, p.Arch)
}

func (p CodexClientProfile) WebviewUserAgent() string {
	return "CodexBrowser Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/" + p.BrowserVersion + " Safari/537.36"
}

func (p CodexClientProfile) ElectronUserAgent() string {
	return strings.TrimPrefix(p.WebviewUserAgent(), "CodexBrowser ") + " (Codex Desktop; " + p.AppVersion + ")"
}

func (p CodexClientProfile) BrowserClientHints() string {
	major, _, _ := strings.Cut(p.BrowserVersion, ".")
	return `"Chromium";v="` + major + `", "CodexBrowser";v="` + major + `"`
}

func (p CodexClientProfile) AcceptLanguage() string {
	base, _, _ := strings.Cut(p.Locale, "-")
	return p.Locale + "," + base + ";q=0.9"
}

func (p CodexClientProfile) cacheKey() string {
	raw, _ := json.Marshal(p)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (p CodexClientProfile) extraValue() map[string]any {
	raw, _ := json.Marshal(p)
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return value
}

var codexClientVersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[.-][A-Za-z0-9]+)*$`)

func parseCodexClientProfile(raw any) (CodexClientProfile, error) {
	var p CodexClientProfile
	data, err := json.Marshal(raw)
	if err != nil || len(data) > 4096 {
		return p, fmt.Errorf("invalid client profile object")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid client profile fields")
	}
	// Persisted templates retain the captured environment. Egress locale/zone
	// are applied separately to a request-local copy after this validation.
	if p.SchemaVersion != 1 || p.TemplateID == "" || len(p.TemplateID) > 64 ||
		strings.ContainsAny(p.TemplateID, "\r\n\x00") || p.OS != "Windows" || p.Arch != "x86_64" ||
		(p.Locale != "zh-CN" && p.Locale != "en-US") || p.Timezone != "Asia/Shanghai" ||
		p.ScreenSizeSum < 1000 || p.ScreenSizeSum > 20000 ||
		!(p.ScreenScale >= 0.5 && p.ScreenScale <= 4) {
		return p, fmt.Errorf("unsupported client environment")
	}
	for _, id := range []string{p.InstallationID, p.DeviceID} {
		value, err := uuid.Parse(id)
		if err != nil || value == uuid.Nil || value.Version() != 4 {
			return p, fmt.Errorf("client identity must be a UUIDv4")
		}
	}
	for _, version := range []string{p.CodexVersion, p.AppVersion, p.OSVersion, p.BrowserVersion, p.OTelSDKVersion} {
		if len(version) > 64 || !codexClientVersionPattern.MatchString(version) {
			return p, fmt.Errorf("invalid client version")
		}
	}
	return p, nil
}

// EnsureCodexClientProfile runs on creation/import and ordinary account
// updates, never on the inference hot path. It owns only this extra key.
func EnsureCodexClientProfile(account *Account) error {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	var p CodexClientProfile
	if raw, exists := account.Extra[CodexClientProfileExtraKey]; exists {
		var err error
		p, err = parseCodexClientProfile(raw)
		if err != nil {
			return infraerrors.BadRequest("OPENAI_CODEX_CLIENT_PROFILE_INVALID", err.Error())
		}
	} else {
		p = newCodexClientProfile(account)
	}
	p = upgradeCapturedCodexClientProfile(p)
	account.Extra = maps.Clone(account.Extra)
	if account.Extra == nil {
		account.Extra = make(map[string]any)
	}
	account.Extra[CodexClientProfileExtraKey] = p.extraValue()
	return nil
}

// Imported account records are new installations even when their source JSON
// contains another account's profile. The environment may be preserved, IDs
// must be newly allocated before the INSERT.
func InitializeCodexClientProfile(account *Account) error {
	if err := EnsureCodexClientProfile(account); err != nil {
		return err
	}
	if account != nil && account.IsOpenAIOAuth() {
		p := persistedCodexClientProfileForAccount(account)
		p.InstallationID, p.DeviceID = uuid.NewString(), uuid.NewString()
		account.Extra[CodexClientProfileExtraKey] = p.extraValue()
	}
	return nil
}

// PreserveCodexClientProfileUpdate retains account identity when another
// update replaces Extra, and rejects an identity copied from another account.
func PreserveCodexClientProfileUpdate(account *Account, extra map[string]any) error {
	if account == nil || !account.IsOpenAIOAuth() {
		return nil
	}
	if raw, exists := extra[CodexClientProfileExtraKey]; exists {
		p, err := parseCodexClientProfile(raw)
		if err != nil {
			return infraerrors.BadRequest("OPENAI_CODEX_CLIENT_PROFILE_INVALID", err.Error())
		}
		current := persistedCodexClientProfileForAccount(account)
		if p.InstallationID != current.InstallationID || p.DeviceID != current.DeviceID {
			return infraerrors.BadRequest("OPENAI_CODEX_CLIENT_IDENTITY_IMMUTABLE", "client installation and device IDs belong to this account and cannot be replaced")
		}
	} else if raw, exists := account.Extra[CodexClientProfileExtraKey]; exists {
		extra[CodexClientProfileExtraKey] = raw
	}
	return nil
}

func codexClientProfileForAccount(account *Account) CodexClientProfile {
	p := persistedCodexClientProfileForAccount(account)
	if account != nil && account.IsOpenAIOAuth() && account.codexEgressProfile != nil {
		region := account.codexEgressProfile
		if region.locale != "" && region.timezone != "" {
			p.Locale, p.Timezone = region.locale, region.timezone
		}
	}
	return p
}

func persistedCodexClientProfileForAccount(account *Account) CodexClientProfile {
	if account != nil && account.IsOpenAIOAuth() {
		if p, err := parseCodexClientProfile(account.Extra[CodexClientProfileExtraKey]); err == nil {
			return upgradeCapturedCodexClientProfile(p)
		}
	}
	// Legacy snapshots may be served briefly by scheduler caches after deploy.
	// Keep the captured environment and identity until the DB refresh.
	p := defaultCodexClientProfile()
	if account != nil {
		p.InstallationID = codexInstallationIDForAccount(account.ID, account.GetChatGPTAccountID())
		p.DeviceID = codexUUIDv4FromSeed("sub2api:codex-device:" + codexAccountSeed(account.ID, account.GetChatGPTAccountID()))
	}
	return p
}

// Migration 240 persists this same release-only upgrade. Apply it to cached or
// imported legacy snapshots too, without rotating identities or custom profiles.
func upgradeCapturedCodexClientProfile(p CodexClientProfile) CodexClientProfile {
	knownTemplate := p.TemplateID == "capture-20260917"
	if suffix, ok := strings.CutPrefix(p.TemplateID, "windows-v1-"); ok && len(suffix) == 2 {
		index, err := strconv.Atoi(suffix)
		knownTemplate = err == nil && suffix[0] >= '0' && suffix[0] <= '1' && index >= 0 && index < 16
	}
	if knownTemplate && p.CodexVersion == "0.155.0-alpha.2.6" && p.AppVersion == "26.911.61220" &&
		p.BrowserVersion == "153.0.0.0" && p.OTelSDKVersion == "0.31.0" {
		p.CodexVersion, p.AppVersion = "0.155.0-alpha.16", "26.917.51856"
		p.TemplateID = strings.Replace(p.TemplateID, "windows-v1-", "windows-v2-", 1)
		if p.TemplateID == "capture-20260917" {
			p.TemplateID = "capture-20260923"
		}
	}
	knownTemplate = p.TemplateID == "capture-20260923"
	if suffix, ok := strings.CutPrefix(p.TemplateID, "windows-v2-"); ok && len(suffix) == 2 {
		index, err := strconv.Atoi(suffix)
		knownTemplate = err == nil && suffix[0] >= '0' && suffix[0] <= '1' && index >= 0 && index < 16
	}
	if knownTemplate && p.CodexVersion == "0.155.0-alpha.16" && p.AppVersion == "26.917.51856" &&
		p.BrowserVersion == "153.0.0.0" && p.OTelSDKVersion == "0.31.0" {
		p.CodexVersion, p.AppVersion = "0.159.0", "26.928.20755"
		p.BrowserVersion = "154.0.0.0"
		p.TemplateID = strings.Replace(p.TemplateID, "windows-v2-", "windows-v3-", 1)
		if p.TemplateID == "capture-20260923" {
			p.TemplateID = "capture-20260930"
		}
	}
	knownTemplate = p.TemplateID == "capture-20260930"
	if suffix, ok := strings.CutPrefix(p.TemplateID, "windows-v3-"); ok && len(suffix) == 2 {
		index, err := strconv.Atoi(suffix)
		knownTemplate = err == nil && suffix[0] >= '0' && suffix[0] <= '1' && index >= 0 && index < 16
	}
	if knownTemplate && p.CodexVersion == "0.159.0" && p.AppVersion == "26.928.20755" &&
		p.BrowserVersion == "154.0.0.0" && p.OTelSDKVersion == "0.31.0" {
		p.CodexVersion, p.AppVersion = codexDesktopVersion, codexDesktopAppVersion
		p.BrowserVersion = "156.0.0.0"
		p.TemplateID = strings.Replace(p.TemplateID, "windows-v3-", "windows-v4-", 1)
		if p.TemplateID == "capture-20260930" {
			p.TemplateID = "capture-20261028"
		}
	}
	return p
}

func codexAccountDeviceProfile(account *Account) *codexDeviceProfile {
	p := codexClientProfileForAccount(account)
	base := codexDeviceProfileForAccount(account.ID, account.GetChatGPTAccountID())
	device := *base
	device.InstallationID = p.InstallationID
	device.Languages, device.Locale, device.Timezone = []string{p.Locale}, p.Locale, p.Timezone
	device.ScreenSizeSum, device.ScreenScale = p.ScreenSizeSum, p.ScreenScale
	device.Attestation = buildCodexOAIAttestation(&device)
	return &device
}

type codexClientContextKey struct{}
type codexClientContext struct {
	accountID int64
	profile   CodexClientProfile
}

func withCodexClientProfile(ctx context.Context, account *Account) context.Context {
	if account == nil || !account.IsOpenAIOAuth() {
		return ctx
	}
	return context.WithValue(ctx, codexClientContextKey{}, codexClientContext{account.ID, codexClientProfileForAccount(account)})
}

func codexClientProfileFromContext(ctx context.Context, accountID int64) CodexClientProfile {
	if value, ok := ctx.Value(codexClientContextKey{}).(codexClientContext); ok && value.accountID == accountID {
		return value.profile
	}
	return defaultCodexClientProfile()
}
