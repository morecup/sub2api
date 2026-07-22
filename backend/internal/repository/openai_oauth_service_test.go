package repository

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type OpenAIOAuthServiceSuite struct {
	suite.Suite
	ctx      context.Context
	srv      *httptest.Server
	svc      *openaiOAuthService
	received chan url.Values
}

func (s *OpenAIOAuthServiceSuite) SetupTest() {
	s.ctx = context.Background()
	s.received = make(chan url.Values, 1)
}

func (s *OpenAIOAuthServiceSuite) TearDownTest() {
	if s.srv != nil {
		s.srv.Close()
		s.srv = nil
	}
}

func (s *OpenAIOAuthServiceSuite) setupServer(handler http.HandlerFunc) {
	s.srv = newLocalTestServer(s.T(), handler)
	s.svc = &openaiOAuthService{tokenURL: s.srv.URL}
}

// decodeOAuthJSONBody 按实抓的 JSON 形态解析 refresh 请求体，
// 并校验请求画像头（content-type/accept/originator/user-agent）。
func decodeOAuthJSONBody(t *testing.T, r *http.Request) map[string]string {
	t.Helper()
	require.Equal(t, "application/json", r.Header.Get("Content-Type"))
	require.Equal(t, "*/*", r.Header.Get("Accept"))
	require.Equal(t, openAIOAuthOriginator, r.Header.Get("Originator"))
	require.Equal(t, openAIOAuthUserAgent, r.Header.Get("User-Agent"))
	raw, err := io.ReadAll(r.Body)
	require.NoError(t, err)
	var body map[string]string
	require.NoError(t, json.Unmarshal(raw, &body))
	return body
}

// decodeOAuthExchangeForm 按实抓形态解析 authorization_code 交换请求：
// form-urlencoded、accept: */*、无 originator/user-agent 头。返回解析后的表单。
func decodeOAuthExchangeForm(t *testing.T, r *http.Request) url.Values {
	t.Helper()
	require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
	require.Equal(t, "*/*", r.Header.Get("Accept"))
	require.Empty(t, r.Header.Get("Originator"))
	require.Empty(t, r.Header.Get("User-Agent"))
	require.NoError(t, r.ParseForm())
	return r.PostForm
}

func (s *OpenAIOAuthServiceSuite) TestExchangeCode_DefaultRedirectURI() {
	errCh := make(chan string, 1)
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			errCh <- "method mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		form := decodeOAuthExchangeForm(s.T(), r)
		// 实抓字段顺序：grant_type, code, redirect_uri, client_id, code_verifier。
		if got := form.Get("grant_type"); got != "authorization_code" {
			errCh <- "grant_type mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := form.Get("client_id"); got != openai.ClientID {
			errCh <- "client_id mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := form.Get("code"); got != "code" {
			errCh <- "code mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := form.Get("redirect_uri"); got != openai.DefaultRedirectURI {
			errCh <- "redirect_uri mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := form.Get("code_verifier"); got != "ver" {
			errCh <- "code_verifier mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","token_type":"bearer","expires_in":3600}`)
	}))

	resp, err := s.svc.ExchangeCode(s.ctx, "code", "ver", "", "", "")
	require.NoError(s.T(), err, "ExchangeCode")
	select {
	case msg := <-errCh:
		require.Fail(s.T(), msg)
	default:
	}
	require.Equal(s.T(), "at", resp.AccessToken)
	require.Equal(s.T(), "rt", resp.RefreshToken)
}

func (s *OpenAIOAuthServiceSuite) TestRefreshToken_FormFields() {
	errCh := make(chan string, 1)
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeOAuthJSONBody(s.T(), r)
		if got := body["grant_type"]; got != "refresh_token" {
			errCh <- "grant_type mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := body["refresh_token"]; got != "rt" {
			errCh <- "refresh_token mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := body["client_id"]; got != openai.ClientID {
			errCh <- "client_id mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// 实抓：refresh 请求不携带 scope。
		if _, exists := body["scope"]; exists {
			errCh <- "scope must be omitted on refresh"
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at2","refresh_token":"rt2","token_type":"bearer","expires_in":3600}`)
	}))

	resp, err := s.svc.RefreshToken(s.ctx, "rt", "")
	require.NoError(s.T(), err, "RefreshToken")
	select {
	case msg := <-errCh:
		require.Fail(s.T(), msg)
	default:
	}
	require.Equal(s.T(), "at2", resp.AccessToken)
	require.Equal(s.T(), "rt2", resp.RefreshToken)
}

// TestRefreshToken_DefaultsToOpenAIClientID 验证未指定 client_id 时默认使用 OpenAI ClientID，
// 且只发送一次请求（不再盲猜多个 client_id）。
func (s *OpenAIOAuthServiceSuite) TestRefreshToken_DefaultsToOpenAIClientID() {
	var seenClientIDs []string
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeOAuthJSONBody(s.T(), r)
		seenClientIDs = append(seenClientIDs, body["client_id"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","refresh_token":"rt","token_type":"bearer","expires_in":3600}`)
	}))

	resp, err := s.svc.RefreshToken(s.ctx, "rt", "")
	require.NoError(s.T(), err, "RefreshToken")
	require.Equal(s.T(), "at", resp.AccessToken)
	// 只发送了一次请求，使用默认的 OpenAI ClientID
	require.Equal(s.T(), []string{openai.ClientID}, seenClientIDs)
}

func (s *OpenAIOAuthServiceSuite) TestRefreshToken_UseProvidedClientID() {
	const customClientID = "custom-client-id"
	var seenClientIDs []string
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := decodeOAuthJSONBody(s.T(), r)
		seenClientIDs = append(seenClientIDs, body["client_id"])
		if body["client_id"] != customClientID {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at-custom","refresh_token":"rt-custom","token_type":"bearer","expires_in":3600}`)
	}))

	resp, err := s.svc.RefreshTokenWithClientID(s.ctx, "rt", "", customClientID)
	require.NoError(s.T(), err, "RefreshTokenWithClientID")
	require.Equal(s.T(), "at-custom", resp.AccessToken)
	require.Equal(s.T(), "rt-custom", resp.RefreshToken)
	require.Equal(s.T(), []string{customClientID}, seenClientIDs)
}

func (s *OpenAIOAuthServiceSuite) TestNonSuccessStatus_IncludesBody() {
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, "bad")
	}))

	_, err := s.svc.ExchangeCode(s.ctx, "code", "ver", openai.DefaultRedirectURI, "", "")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "status 400")
	require.ErrorContains(s.T(), err, "bad")
}

func (s *OpenAIOAuthServiceSuite) TestRequestError_ClosedServer() {
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.srv.Close()

	_, err := s.svc.ExchangeCode(s.ctx, "code", "ver", openai.DefaultRedirectURI, "", "")
	require.Error(s.T(), err)
	require.ErrorContains(s.T(), err, "request failed")
}

func (s *OpenAIOAuthServiceSuite) TestExchangeCode_RequestErrorWithoutProxyReturnsProxyHint() {
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	s.srv.Close()

	_, err := s.svc.ExchangeCode(s.ctx, "code", "ver", openai.DefaultRedirectURI, "", "")

	require.Error(s.T(), err)
	require.Equal(s.T(), "OPENAI_OAUTH_PROXY_REQUIRED", infraerrors.Reason(err))
	require.Contains(s.T(), infraerrors.Message(err), "no proxy is configured")
}

func (s *OpenAIOAuthServiceSuite) TestContextCancel() {
	started := make(chan struct{})
	block := make(chan struct{})
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-block
	}))

	ctx, cancel := context.WithCancel(s.ctx)

	done := make(chan error, 1)
	go func() {
		_, err := s.svc.ExchangeCode(ctx, "code", "ver", openai.DefaultRedirectURI, "", "")
		done <- err
	}()

	<-started
	cancel()
	close(block)

	err := <-done
	require.Error(s.T(), err)
}

func (s *OpenAIOAuthServiceSuite) TestExchangeCode_UsesProvidedRedirectURI() {
	want := "http://localhost:9999/cb"
	errCh := make(chan string, 1)
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form := decodeOAuthExchangeForm(s.T(), r)
		if got := form.Get("redirect_uri"); got != want {
			errCh <- "redirect_uri mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"bearer","expires_in":1}`)
	}))

	_, err := s.svc.ExchangeCode(s.ctx, "code", "ver", want, "", "")
	require.NoError(s.T(), err, "ExchangeCode")
	select {
	case msg := <-errCh:
		require.Fail(s.T(), msg)
	default:
	}
}

func (s *OpenAIOAuthServiceSuite) TestExchangeCode_UseProvidedClientID() {
	wantClientID := "custom-exchange-client-id"
	errCh := make(chan string, 1)
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		form := decodeOAuthExchangeForm(s.T(), r)
		if got := form.Get("client_id"); got != wantClientID {
			errCh <- "client_id mismatch"
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"bearer","expires_in":1}`)
	}))

	_, err := s.svc.ExchangeCode(s.ctx, "code", "ver", openai.DefaultRedirectURI, "", wantClientID)
	require.NoError(s.T(), err, "ExchangeCode")
	select {
	case msg := <-errCh:
		require.Fail(s.T(), msg)
	default:
	}
}

func (s *OpenAIOAuthServiceSuite) TestTokenURL_CanBeOverriddenWithQuery() {
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		s.received <- url.Values{"body": {string(raw)}}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"bearer","expires_in":1}`)
	}))
	s.svc.tokenURL = s.srv.URL + "?x=1"

	_, err := s.svc.ExchangeCode(s.ctx, "code", "ver", openai.DefaultRedirectURI, "", "")
	require.NoError(s.T(), err, "ExchangeCode")
	select {
	case <-s.received:
	default:
		require.Fail(s.T(), "expected server to receive request")
	}
}

func (s *OpenAIOAuthServiceSuite) TestExchangeCode_SuccessButInvalidJSON() {
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "not-valid-json")
	}))

	_, err := s.svc.ExchangeCode(s.ctx, "code", "ver", openai.DefaultRedirectURI, "", "")
	require.Error(s.T(), err, "expected error for invalid JSON response")
}

func (s *OpenAIOAuthServiceSuite) TestRefreshToken_NonSuccessStatus() {
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "unauthorized")
	}))

	_, err := s.svc.RefreshToken(s.ctx, "rt", "")
	require.Error(s.T(), err, "expected error for non-2xx status")
	require.ErrorContains(s.T(), err, "status 401")
}

func (s *OpenAIOAuthServiceSuite) TestExchangeCode_FormFieldOrder() {
	// 实抓（auth_login_exchange_flows.txt）：form 字段顺序为
	// grant_type, code, redirect_uri, client_id, code_verifier。
	bodyCh := make(chan string, 1)
	s.setupServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		bodyCh <- string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"at","token_type":"bearer","expires_in":1}`)
	}))

	_, err := s.svc.ExchangeCode(s.ctx, "CODE X", "VER/1", openai.DefaultRedirectURI, "", "")
	require.NoError(s.T(), err)
	got := <-bodyCh
	want := "grant_type=authorization_code" +
		"&code=CODE+X" +
		"&redirect_uri=" + url.QueryEscape(openai.DefaultRedirectURI) +
		"&client_id=" + openai.ClientID +
		"&code_verifier=" + url.QueryEscape("VER/1")
	require.Equal(s.T(), want, got)
}

func TestNewOpenAIOAuthClient_DefaultTokenURL(t *testing.T) {
	client := NewOpenAIOAuthClient()
	svc, ok := client.(*openaiOAuthService)
	require.True(t, ok)
	require.Equal(t, openai.TokenURL, svc.tokenURL)
}

func TestOpenAIOAuthServiceSuite(t *testing.T) {
	suite.Run(t, new(OpenAIOAuthServiceSuite))
}
