package service

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/require"
)

func TestAccountTLSFingerprintDefaultsEnabledForAnthropicOAuth(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{
			name: "oauth nil extra defaults enabled",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
			},
			want: true,
		},
		{
			name: "setup token missing flag defaults enabled",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeSetupToken,
				Extra:    map[string]any{},
			},
			want: true,
		},
		{
			name: "explicit true enabled",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"enable_tls_fingerprint": true},
			},
			want: true,
		},
		{
			name: "explicit false disabled",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"enable_tls_fingerprint": false},
			},
			want: false,
		},
		{
			name: "non bool flag falls back to enabled",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeOAuth,
				Extra:    map[string]any{"enable_tls_fingerprint": "false"},
			},
			want: true,
		},
		{
			name: "anthropic api key unsupported",
			account: &Account{
				Platform: PlatformAnthropic,
				Type:     AccountTypeAPIKey,
			},
			want: false,
		},
		{
			name: "openai oauth unsupported",
			account: &Account{
				Platform: PlatformOpenAI,
				Type:     AccountTypeOAuth,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, tt.account.IsTLSFingerprintEnabled())
		})
	}
}

func TestResolveTLSProfileDefaultsToBuiltInForAnthropicOAuth(t *testing.T) {
	svc := &TLSFingerprintProfileService{}
	profile := svc.ResolveTLSProfile(&Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
	})

	require.NotNil(t, profile)
	require.Equal(t, tlsfingerprint.BuiltInDefaultProfileName, profile.Name)

	disabled := svc.ResolveTLSProfile(&Account{
		Platform: PlatformAnthropic,
		Type:     AccountTypeOAuth,
		Extra:    map[string]any{"enable_tls_fingerprint": false},
	})
	require.Nil(t, disabled)
}
