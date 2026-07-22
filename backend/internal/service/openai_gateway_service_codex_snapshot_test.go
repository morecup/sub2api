package service

import (
	"net/http"
	"testing"
	"time"
)

func TestCodexSnapshotBaseTime(t *testing.T) {
	fallback := time.Date(2026, 2, 20, 9, 0, 0, 0, time.UTC)

	t.Run("nil snapshot uses fallback", func(t *testing.T) {
		got := codexSnapshotBaseTime(nil, fallback)
		if !got.Equal(fallback) {
			t.Fatalf("got %v, want fallback %v", got, fallback)
		}
	})

	t.Run("empty updatedAt uses fallback", func(t *testing.T) {
		got := codexSnapshotBaseTime(&OpenAICodexUsageSnapshot{}, fallback)
		if !got.Equal(fallback) {
			t.Fatalf("got %v, want fallback %v", got, fallback)
		}
	})

	t.Run("valid updatedAt wins", func(t *testing.T) {
		got := codexSnapshotBaseTime(&OpenAICodexUsageSnapshot{UpdatedAt: "2026-02-16T10:00:00Z"}, fallback)
		want := time.Date(2026, 2, 16, 10, 0, 0, 0, time.UTC)
		if !got.Equal(want) {
			t.Fatalf("got %v, want %v", got, want)
		}
	})

	t.Run("invalid updatedAt uses fallback", func(t *testing.T) {
		got := codexSnapshotBaseTime(&OpenAICodexUsageSnapshot{UpdatedAt: "invalid"}, fallback)
		if !got.Equal(fallback) {
			t.Fatalf("got %v, want fallback %v", got, fallback)
		}
	})
}

func TestCodexResetAtRFC3339(t *testing.T) {
	base := time.Date(2026, 2, 16, 10, 0, 0, 0, time.UTC)

	t.Run("nil reset returns nil", func(t *testing.T) {
		if got := codexResetAtRFC3339(base, nil); got != nil {
			t.Fatalf("expected nil, got %v", *got)
		}
	})

	t.Run("positive seconds", func(t *testing.T) {
		sec := 90
		got := codexResetAtRFC3339(base, &sec)
		if got == nil {
			t.Fatal("expected non-nil")
		}
		if *got != "2026-02-16T10:01:30Z" {
			t.Fatalf("got %s, want %s", *got, "2026-02-16T10:01:30Z")
		}
	})

	t.Run("negative seconds clamp to base", func(t *testing.T) {
		sec := -3
		got := codexResetAtRFC3339(base, &sec)
		if got == nil {
			t.Fatal("expected non-nil")
		}
		if *got != "2026-02-16T10:00:00Z" {
			t.Fatalf("got %s, want %s", *got, "2026-02-16T10:00:00Z")
		}
	})
}

func TestBuildCodexUsageExtraUpdates_UsesSnapshotUpdatedAt(t *testing.T) {
	primaryUsed := 88.0
	primaryReset := 86400
	primaryWindow := 10080
	secondaryUsed := 12.0
	secondaryReset := 3600
	secondaryWindow := 300

	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         &primaryUsed,
		PrimaryResetAfterSeconds:   &primaryReset,
		PrimaryWindowMinutes:       &primaryWindow,
		SecondaryUsedPercent:       &secondaryUsed,
		SecondaryResetAfterSeconds: &secondaryReset,
		SecondaryWindowMinutes:     &secondaryWindow,
		UpdatedAt:                  "2026-02-16T10:00:00Z",
	}

	updates := buildCodexUsageExtraUpdates(snapshot, time.Date(2026, 2, 20, 8, 0, 0, 0, time.UTC))
	if updates == nil {
		t.Fatal("expected non-nil updates")
	}

	if got := updates["codex_usage_updated_at"]; got != "2026-02-16T10:00:00Z" {
		t.Fatalf("codex_usage_updated_at = %v, want %s", got, "2026-02-16T10:00:00Z")
	}
	if got := updates["codex_5h_reset_at"]; got != "2026-02-16T11:00:00Z" {
		t.Fatalf("codex_5h_reset_at = %v, want %s", got, "2026-02-16T11:00:00Z")
	}
	if got := updates["codex_7d_reset_at"]; got != "2026-02-17T10:00:00Z" {
		t.Fatalf("codex_7d_reset_at = %v, want %s", got, "2026-02-17T10:00:00Z")
	}
}

func TestBuildCodexUsageExtraUpdates_LatestDesktop30DayWindowIsNot5hOr7d(t *testing.T) {
	// Codex Desktop 0.145.0-alpha.27 实抓响应头：primary=43200 分钟（30 天），
	// secondary=0（未启用）。二者都不能被旧的大小比较逻辑误标成 7d/5h。
	headers := http.Header{}
	headers.Set("x-codex-primary-used-percent", "2")
	headers.Set("x-codex-secondary-used-percent", "0")
	headers.Set("x-codex-primary-window-minutes", "43200")
	headers.Set("x-codex-secondary-window-minutes", "0")
	headers.Set("x-codex-primary-reset-after-seconds", "2590694")
	headers.Set("x-codex-secondary-reset-after-seconds", "0")

	snapshot := ParseCodexRateLimitHeaders(headers)
	if snapshot == nil {
		t.Fatal("expected parsed snapshot")
	}
	if normalized := snapshot.Normalize(); normalized != nil {
		t.Fatalf("30-day/disabled windows must not normalize to 5h/7d: %+v", normalized)
	}

	updates := buildCodexUsageExtraUpdates(snapshot, time.Date(2026, 7, 21, 16, 20, 12, 0, time.UTC))
	if got := updates["codex_primary_window_minutes"]; got != 43200 {
		t.Fatalf("codex_primary_window_minutes = %v, want 43200", got)
	}
	if got := updates["codex_secondary_window_minutes"]; got != 0 {
		t.Fatalf("codex_secondary_window_minutes = %v, want 0", got)
	}
	for _, key := range codexCanonicalUsageExtraKeys {
		value, ok := updates[key]
		if !ok || value != nil {
			t.Fatalf("%s = %v (present=%v), want cleanup marker nil", key, value, ok)
		}
	}
}

func TestBuildCodexUsageProgressFromExtra_RejectsMislabeled30DayWindow(t *testing.T) {
	extra := map[string]any{
		"codex_7d_used_percent":   2.0,
		"codex_7d_window_minutes": 43200,
	}
	if got := buildCodexUsageProgressFromExtra(extra, "7d", time.Now()); got != nil {
		t.Fatalf("30-day window must not be rendered as 7d: %+v", got)
	}
}

func TestMergeAccountExtra_RemovesCanonicalCleanupMarkers(t *testing.T) {
	account := &Account{Extra: map[string]any{
		"codex_5h_used_percent": 12.0,
		"codex_7d_used_percent": 34.0,
	}}
	mergeAccountExtra(account, map[string]any{
		"codex_primary_window_minutes": 43200,
		"codex_5h_used_percent":        nil,
		"codex_7d_used_percent":        nil,
	})
	if _, ok := account.Extra["codex_5h_used_percent"]; ok {
		t.Fatal("codex_5h_used_percent cleanup marker must delete the in-memory alias")
	}
	if _, ok := account.Extra["codex_7d_used_percent"]; ok {
		t.Fatal("codex_7d_used_percent cleanup marker must delete the in-memory alias")
	}
	if got := account.Extra["codex_primary_window_minutes"]; got != 43200 {
		t.Fatalf("codex_primary_window_minutes = %v, want 43200", got)
	}
}

// TestBuildCodexUsageExtraUpdates_FreshAccountUsedPercentNotInverted_Issue2994 locks in the
// canonical "used %" semantics for the 5h window. A fresh account reports a tiny
// secondary-used-percent (~1%); the stored codex_5h_used_percent must equal that value
// directly and must NOT be inverted to ~99%. Regression guard for issue #2994 / the reverted
// commit b65dde63 (PR #2918), which applied `100 - used` and made fresh accounts look
// exhausted, tripping auto-pause and excluding them from scheduling.
func TestBuildCodexUsageExtraUpdates_FreshAccountUsedPercentNotInverted_Issue2994(t *testing.T) {
	secondaryUsed := 1.0 // 5h window: barely used
	secondaryWindow := 300
	primaryUsed := 2.0 // 7d window: barely used
	primaryWindow := 10080

	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:     &primaryUsed,
		PrimaryWindowMinutes:   &primaryWindow,
		SecondaryUsedPercent:   &secondaryUsed,
		SecondaryWindowMinutes: &secondaryWindow,
		UpdatedAt:              "2026-02-16T10:00:00Z",
	}

	updates := buildCodexUsageExtraUpdates(snapshot, time.Date(2026, 2, 16, 10, 0, 0, 0, time.UTC))
	if updates == nil {
		t.Fatal("expected non-nil updates")
	}

	if got := updates["codex_5h_used_percent"]; got != 1.0 {
		t.Fatalf("codex_5h_used_percent = %v, want 1.0 (direct used%%, NOT inverted to 99)", got)
	}
	if got := updates["codex_7d_used_percent"]; got != 2.0 {
		t.Fatalf("codex_7d_used_percent = %v, want 2.0 (direct used%%, NOT inverted to 98)", got)
	}
}

func TestBuildCodexUsageExtraUpdates_FallbackToNowWhenUpdatedAtInvalid(t *testing.T) {
	primaryUsed := 15.0
	primaryReset := 30
	primaryWindow := 300

	fallbackNow := time.Date(2026, 2, 20, 8, 30, 0, 0, time.UTC)
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:       &primaryUsed,
		PrimaryResetAfterSeconds: &primaryReset,
		PrimaryWindowMinutes:     &primaryWindow,
		UpdatedAt:                "invalid-time",
	}

	updates := buildCodexUsageExtraUpdates(snapshot, fallbackNow)
	if updates == nil {
		t.Fatal("expected non-nil updates")
	}

	if got := updates["codex_usage_updated_at"]; got != "2026-02-20T08:30:00Z" {
		t.Fatalf("codex_usage_updated_at = %v, want %s", got, "2026-02-20T08:30:00Z")
	}
	if got := updates["codex_5h_reset_at"]; got != "2026-02-20T08:30:30Z" {
		t.Fatalf("codex_5h_reset_at = %v, want %s", got, "2026-02-20T08:30:30Z")
	}
}

func TestBuildCodexUsageExtraUpdates_ClampNegativeResetSeconds(t *testing.T) {
	primaryUsed := 90.0
	primaryReset := 7200
	primaryWindow := 10080
	secondaryUsed := 100.0
	secondaryReset := -15
	secondaryWindow := 300

	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent:         &primaryUsed,
		PrimaryResetAfterSeconds:   &primaryReset,
		PrimaryWindowMinutes:       &primaryWindow,
		SecondaryUsedPercent:       &secondaryUsed,
		SecondaryResetAfterSeconds: &secondaryReset,
		SecondaryWindowMinutes:     &secondaryWindow,
		UpdatedAt:                  "2026-02-16T10:00:00Z",
	}

	updates := buildCodexUsageExtraUpdates(snapshot, time.Time{})
	if updates == nil {
		t.Fatal("expected non-nil updates")
	}

	if got := updates["codex_5h_reset_after_seconds"]; got != -15 {
		t.Fatalf("codex_5h_reset_after_seconds = %v, want %d", got, -15)
	}
	if got := updates["codex_5h_reset_at"]; got != "2026-02-16T10:00:00Z" {
		t.Fatalf("codex_5h_reset_at = %v, want %s", got, "2026-02-16T10:00:00Z")
	}
}

func TestBuildCodexUsageExtraUpdates_NilSnapshot(t *testing.T) {
	if got := buildCodexUsageExtraUpdates(nil, time.Now()); got != nil {
		t.Fatalf("expected nil updates, got %v", got)
	}
}

func TestBuildCodexUsageExtraUpdates_WithoutNormalizedWindowFields(t *testing.T) {
	primaryUsed := 42.0
	fallbackNow := time.Date(2026, 2, 20, 9, 15, 0, 0, time.UTC)
	snapshot := &OpenAICodexUsageSnapshot{
		PrimaryUsedPercent: &primaryUsed,
		UpdatedAt:          "",
	}

	updates := buildCodexUsageExtraUpdates(snapshot, fallbackNow)
	if updates == nil {
		t.Fatal("expected non-nil updates")
	}

	if got := updates["codex_usage_updated_at"]; got != "2026-02-20T09:15:00Z" {
		t.Fatalf("codex_usage_updated_at = %v, want %s", got, "2026-02-20T09:15:00Z")
	}
	if _, ok := updates["codex_5h_reset_at"]; ok {
		t.Fatalf("did not expect codex_5h_reset_at in updates: %v", updates["codex_5h_reset_at"])
	}
	if _, ok := updates["codex_7d_reset_at"]; ok {
		t.Fatalf("did not expect codex_7d_reset_at in updates: %v", updates["codex_7d_reset_at"])
	}
}
