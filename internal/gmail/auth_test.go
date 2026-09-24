package gmail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestSaveTokenUsesOwnerOnlyPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")

	if err := SaveToken(path, &oauth2.Token{RefreshToken: "r"}); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("token file mode = %o, want 600", got)
	}
}

func TestLoadTokenRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	want := &oauth2.Token{AccessToken: "a", RefreshToken: "r", TokenType: "Bearer"}

	if err := SaveToken(path, want); err != nil {
		t.Fatalf("SaveToken() error = %v", err)
	}
	got, err := LoadToken(path)
	if err != nil {
		t.Fatalf("LoadToken() error = %v", err)
	}
	if got.RefreshToken != want.RefreshToken || got.AccessToken != want.AccessToken {
		t.Errorf("LoadToken() = %+v, want %+v", got, want)
	}
}

func TestLoadTokenRejectsMissingFile(t *testing.T) {
	if _, err := LoadToken(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("LoadToken() error = nil, want an error")
	}
}

func tokenServer(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func expiredSource(srv *httptest.Server) oauth2.TokenSource {
	cfg := &oauth2.Config{
		ClientID:     "id",
		ClientSecret: "secret",
		Endpoint:     oauth2.Endpoint{TokenURL: srv.URL},
	}
	return cfg.TokenSource(context.Background(), &oauth2.Token{
		RefreshToken: "refresh",
		Expiry:       time.Now().Add(-time.Hour),
	})
}

func TestCheckTokenSourceReportsExpiredToken(t *testing.T) {
	srv := tokenServer(t, http.StatusBadRequest,
		`{"error":"invalid_grant","error_description":"Token has been expired or revoked."}`)

	err := checkTokenSource(expiredSource(srv))
	if !errors.Is(err, ErrTokenExpired) {
		t.Errorf("checkTokenSource() = %v, want ErrTokenExpired", err)
	}
}

func TestCheckTokenSourceAcceptsFreshToken(t *testing.T) {
	srv := tokenServer(t, http.StatusOK,
		`{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`)

	if err := checkTokenSource(expiredSource(srv)); err != nil {
		t.Errorf("checkTokenSource() = %v, want nil", err)
	}
}

func TestCheckTokenSourceReportsOtherFailures(t *testing.T) {
	srv := tokenServer(t, http.StatusInternalServerError, `{"error":"backend_error"}`)

	err := checkTokenSource(expiredSource(srv))
	if err == nil {
		t.Fatal("checkTokenSource() = nil, want an error")
	}
	if errors.Is(err, ErrTokenExpired) {
		t.Error("checkTokenSource() = ErrTokenExpired, want a generic error")
	}
}

func TestLoadTokenRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(path); err == nil {
		t.Fatal("LoadToken() error = nil, want a parse error")
	}
}

func TestCredentialsConfigReadsDesktopClient(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client_secret.json")
	body, _ := json.Marshal(map[string]any{
		"installed": map[string]any{
			"client_id":     "id.apps.googleusercontent.com",
			"client_secret": "secret",
			"auth_uri":      "https://accounts.google.com/o/oauth2/auth",
			"token_uri":     "https://oauth2.googleapis.com/token",
			"redirect_uris": []string{"http://localhost"},
		},
	})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := credentialsConfig(path)
	if err != nil {
		t.Fatalf("credentialsConfig() error = %v", err)
	}
	if cfg.ClientID != "id.apps.googleusercontent.com" {
		t.Errorf("ClientID = %q, want the desktop client id", cfg.ClientID)
	}
	if len(cfg.Scopes) != 1 || cfg.Scopes[0] != "https://www.googleapis.com/auth/gmail.modify" {
		t.Errorf("Scopes = %v, want exactly gmail.modify", cfg.Scopes)
	}
}

func TestCredentialsConfigRejectsMissingFile(t *testing.T) {
	if _, err := credentialsConfig(filepath.Join(t.TempDir(), "absent.json")); err == nil {
		t.Fatal("credentialsConfig() error = nil, want an error")
	}
}

// syncBuffer is a goroutine-safe io.Writer that captures Authorize's prompt.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// writeCredentials writes a Desktop-client credentials file and returns its path.
func writeCredentials(t *testing.T) string {
	t.Helper()
	return writeCredentialsWithTokenURI(t, "https://oauth2.googleapis.com/token")
}

// writeCredentialsWithTokenURI writes credentials whose token endpoint is
// tokenURI, so a test can observe whether Authorize ever attempts an exchange.
func writeCredentialsWithTokenURI(t *testing.T, tokenURI string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "client_secret.json")
	body, _ := json.Marshal(map[string]any{
		"installed": map[string]any{
			"client_id":     "id.apps.googleusercontent.com",
			"client_secret": "secret",
			"auth_uri":      "https://accounts.google.com/o/oauth2/auth",
			"token_uri":     tokenURI,
			"redirect_uris": []string{"http://localhost"},
		},
	})
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// waitForAuthURL blocks until Authorize prints its consent URL, then returns the
// parsed URL so tests can read both the loopback redirect_uri and the state.
func waitForAuthURL(t *testing.T, out *syncBuffer) *url.URL {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, line := range strings.Split(out.String(), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, "https://accounts.google.com/") {
				continue
			}
			u, err := url.Parse(line)
			if err != nil {
				t.Fatalf("parse auth URL: %v", err)
			}
			if u.Query().Get("redirect_uri") != "" && u.Query().Get("state") != "" {
				return u
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for the consent URL; output so far:\n%s", out.String())
	return nil
}

// callbackWith builds a loopback callback URL carrying the given query values.
func callbackWith(t *testing.T, authURL *url.URL, values url.Values) string {
	t.Helper()
	callback := authURL.Query().Get("redirect_uri")
	if callback == "" {
		t.Fatal("consent URL has no redirect_uri")
	}
	return callback + "?" + values.Encode()
}

func TestAuthorizeReportsDeniedConsent(t *testing.T) {
	creds := writeCredentials(t)
	tokenPath := filepath.Join(t.TempDir(), "token.json")
	out := &syncBuffer{}
	errCh := make(chan error, 1)
	go func() {
		_, err := Authorize(context.Background(), creds, tokenPath, out)
		errCh <- err
	}()

	authURL := waitForAuthURL(t, out)
	callback := callbackWith(t, authURL, url.Values{
		"state": {authURL.Query().Get("state")},
		"error": {"access_denied"},
	})
	resp, err := http.Get(callback)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "authorization denied") {
		t.Errorf("Authorize() error = %v, want an authorization-denied error", err)
	}
	if _, statErr := os.Stat(tokenPath); statErr == nil {
		t.Error("Authorize() wrote a token after denied consent")
	}
}

func TestAuthorizeReportsMissingCode(t *testing.T) {
	creds := writeCredentials(t)
	out := &syncBuffer{}
	errCh := make(chan error, 1)
	go func() {
		_, err := Authorize(context.Background(), creds, filepath.Join(t.TempDir(), "token.json"), out)
		errCh <- err
	}()

	authURL := waitForAuthURL(t, out)
	callback := callbackWith(t, authURL, url.Values{
		"state": {authURL.Query().Get("state")},
	})
	resp, err := http.Get(callback)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "no authorization code") {
		t.Errorf("Authorize() error = %v, want a missing-code error", err)
	}
}

// TestAuthorizeRejectsWrongState drives the callback with a valid-looking code
// but the wrong state. The exchange must never run, so the token endpoint must
// not be contacted and no token file may be written.
func TestAuthorizeRejectsWrongState(t *testing.T) {
	var exchanges atomic.Int32
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		exchanges.Add(1)
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"attacker","token_type":"Bearer","expires_in":3600,"refresh_token":"attacker-refresh"}`)
	}))
	t.Cleanup(tokenSrv.Close)

	creds := writeCredentialsWithTokenURI(t, tokenSrv.URL)
	tokenPath := filepath.Join(t.TempDir(), "token.json")
	out := &syncBuffer{}
	errCh := make(chan error, 1)
	go func() {
		_, err := Authorize(context.Background(), creds, tokenPath, out)
		errCh <- err
	}()

	authURL := waitForAuthURL(t, out)
	callback := callbackWith(t, authURL, url.Values{
		"state": {"not-the-real-state"},
		"code":  {"attacker-code"},
	})
	resp, err := http.Get(callback)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "state") {
		t.Errorf("Authorize() error = %v, want a state-mismatch error", err)
	}
	if got := exchanges.Load(); got != 0 {
		t.Errorf("token endpoint contacted %d time(s), want 0 — the exchange must never run", got)
	}
	if _, statErr := os.Stat(tokenPath); statErr == nil {
		t.Error("Authorize() wrote a token despite a state mismatch")
	}
}

// TestAuthorizeRejectsTokenWithoutRefreshToken drives the full loopback flow
// against a token endpoint that returns an access token with no refresh_token.
// Authorize must fail and write no token file, because a credential without a
// refresh token dies within the hour.
func TestAuthorizeRejectsTokenWithoutRefreshToken(t *testing.T) {
	tokenSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"access_token":"a","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(tokenSrv.Close)

	creds := writeCredentialsWithTokenURI(t, tokenSrv.URL)
	tokenPath := filepath.Join(t.TempDir(), "token.json")
	out := &syncBuffer{}
	errCh := make(chan error, 1)
	go func() {
		_, err := Authorize(context.Background(), creds, tokenPath, out)
		errCh <- err
	}()

	authURL := waitForAuthURL(t, out)
	callback := callbackWith(t, authURL, url.Values{
		"state": {authURL.Query().Get("state")},
		"code":  {"real-code"},
	})
	resp, err := http.Get(callback)
	if err != nil {
		t.Fatalf("GET callback: %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if err := <-errCh; err == nil || !strings.Contains(err.Error(), "no refresh token") {
		t.Errorf("Authorize() error = %v, want a missing-refresh-token error", err)
	}
	if _, statErr := os.Stat(tokenPath); statErr == nil {
		t.Error("Authorize() wrote a token without a refresh token")
	}
}

func TestAuthorizeStopsWhenContextCancelled(t *testing.T) {
	creds := writeCredentials(t)
	out := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := Authorize(ctx, creds, filepath.Join(t.TempDir(), "token.json"), out)
		errCh <- err
	}()

	waitForAuthURL(t, out)
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Authorize() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Authorize() did not return after context cancellation")
	}
}
