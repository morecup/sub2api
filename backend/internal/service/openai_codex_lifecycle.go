package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func validCodexMetadataUUID(raw string) string {
	value, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil || value == uuid.Nil {
		return ""
	}
	return value.String()
}

func codexMetadataTurn(profile codexTurnMetadataProfile) (string, int64) {
	turnID, started := profile.TurnID, profile.TurnStartedAtUnixMs
	if turnID == "" {
		turnID = newCodexUUIDV7()
	}
	if started == 0 {
		started = time.Now().UnixMilli()
	}
	return turnID, started
}

func codexMetadataWindow(sessionID, windowID string, profile codexTurnMetadataProfile) (string, int, string) {
	number := profile.WindowNumber
	if profile.HasWindowNumber {
		// Keep the window index while retaining account/API-key thread isolation.
		windowID = fmt.Sprintf("%s:%d", sessionID, number)
	} else if suffix, ok := strings.CutPrefix(windowID, sessionID+":"); ok {
		if parsed, err := strconv.Atoi(suffix); err == nil && parsed >= 0 && parsed <= 1_000_000 {
			number = parsed
		}
	}
	contextID := profile.ContextWindowID
	if contextID == "" {
		contextID = generateCodexContextWindowUUID(sessionID)
		if number > 0 {
			if source, err := uuid.Parse(contextID); err == nil {
				contextID = isolatedCodexUUIDV7(source, fmt.Sprintf("context-window:%s:%d", sessionID, number))
			}
		}
	}
	return windowID, number, contextID
}

func resolveOpenAITaskSessionID(accountID, apiKeyID int64, seed, fixed string) string {
	if fixed != "" {
		return resolveCodexSessionUUID(accountID, apiKeyID, seed, fixed)
	}
	return isolateOpenAISessionIDForAccount(accountID, apiKeyID, seed)
}

func codexSessionSeedFromMetadata(seed, metadata string) string {
	if strings.TrimSpace(seed) != "" {
		return seed
	}
	var incoming struct {
		ThreadID  string `json:"thread_id"`
		SessionID string `json:"session_id"`
	}
	if json.Unmarshal([]byte(metadata), &incoming) == nil {
		if value := validCodexMetadataUUID(incoming.ThreadID); value != "" {
			return value
		}
		return validCodexMetadataUUID(incoming.SessionID)
	}
	return ""
}

func syncCodexMetadataWindowHeader(headers http.Header) {
	var metadata struct {
		WindowID string `json:"window_id"`
	}
	if json.Unmarshal([]byte(headers.Get("x-codex-turn-metadata")), &metadata) == nil && metadata.WindowID != "" {
		headers.Set("x-codex-window-id", metadata.WindowID)
	}
}

// A WS handshake describes connection setup. Each response.create can carry a
// newer turn/window, so its metadata takes precedence over the handshake.
func openAIWSFrameTurnMetadata(raw []byte, fallback string) string {
	if value := gjson.GetBytes(raw, "client_metadata."+openAIWSTurnMetadataHeader); value.Type == gjson.String && strings.TrimSpace(value.Str) != "" {
		return value.Str
	}
	return fallback
}

func codexRequestTurnMetadata(sessionID, windowID, installationID, inbound string, prewarm bool) string {
	if prewarm {
		return buildCodexWSPrewarmMetadata(sessionID, windowID, installationID, inbound)
	}
	if compaction, ok := extractCodexCompactionRequest(inbound); ok {
		return buildCodexCompactionMetadata(sessionID, windowID, installationID, compaction, inbound)
	}
	return buildCodexTurnMetadata(sessionID, windowID, extractCodexWorkspaces(inbound), installationID, inbound)
}

// Rewrite metadata only, retaining the original tool/history JSON bytes. The
// caller gates this to Codex OAuth traffic; API-key passthrough remains intact.
func syncCodexWSFrameMetadata(raw []byte, headers http.Header, fallback string) ([]byte, error) {
	if gjson.GetBytes(raw, "type").String() != "response.create" || headers.Get("session-id") == "" {
		return raw, nil
	}
	metadata := codexRequestTurnMetadata(headers.Get("session-id"), headers.Get("x-codex-window-id"),
		gjson.Get(headers.Get(openAIWSTurnMetadataHeader), "installation_id").String(),
		openAIWSFrameTurnMetadata(raw, fallback), gjson.GetBytes(raw, "generate").Type == gjson.False)
	var clientMetadata map[string]any
	if existing := gjson.GetBytes(raw, "client_metadata"); existing.IsObject() {
		if err := json.Unmarshal([]byte(existing.Raw), &clientMetadata); err != nil {
			return nil, err
		}
	}
	payload := map[string]any{"client_metadata": clientMetadata}
	applyCodexClientMetadata(payload, "", metadata)
	applyCodexWSRequestClientMetadata(payload, gjson.GetBytes(raw, "model").String())
	encoded, err := json.Marshal(payload["client_metadata"])
	if err != nil {
		return nil, err
	}
	updated, err := sjson.SetRawBytes(raw, "client_metadata", encoded)
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(updated, "prompt_cache_key", payload["prompt_cache_key"])
}
