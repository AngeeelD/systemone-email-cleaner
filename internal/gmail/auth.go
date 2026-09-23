// Package gmail talks to the Gmail API on behalf of one account.
package gmail

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// GmailModifyScope is the only scope this tool requests. It permits reading,
// label changes and Trash. It deliberately does not permit permanent deletion.
const GmailModifyScope = "https://www.googleapis.com/auth/gmail.modify"

// ErrTokenExpired means the operator must re-run `emailcleaner setup`.
var ErrTokenExpired = errors.New("gmail token expired or revoked")

func credentialsConfig(credentialsFile string) (*oauth2.Config, error) {
	b, err := os.ReadFile(credentialsFile)
	if err != nil {
		return nil, fmt.Errorf("read credentials %s: %w", credentialsFile, err)
	}
	cfg, err := google.ConfigFromJSON(b, GmailModifyScope)
	if err != nil {
		return nil, fmt.Errorf("parse credentials %s: %w", credentialsFile, err)
	}
	return cfg, nil
}

// Authorize runs the OAuth loopback flow, prints the consent URL to out, and
// stores the resulting token at tokenFile. Google allows any loopback port for
// Desktop-type clients, so a random free port is used and no fixed port or
// hosted redirect URL is needed.
func Authorize(ctx context.Context, credentialsFile, tokenFile string, out io.Writer) (*oauth2.Token, error) {
	cfg, err := credentialsConfig(credentialsFile)
	if err != nil {
		return nil, err
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start loopback listener: %w", err)
	}
	defer ln.Close()
	cfg.RedirectURL = fmt.Sprintf("http://%s/callback", ln.Addr().String())

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		if e := r.URL.Query().Get("error"); e != "" {
			fmt.Fprintf(w, "Authorization failed: %s. You can close this tab.", e)
			errCh <- fmt.Errorf("authorization denied: %s", e)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			fmt.Fprint(w, "Authorization failed: no code returned. You can close this tab.")
			errCh <- errors.New("callback received no authorization code")
			return
		}
		fmt.Fprint(w, "Authorization complete. You can close this tab.")
		codeCh <- code
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// AccessTypeOffline requests a refresh token; prompt=consent forces Google
	// to re-issue one, which is required on every re-authorization.
	authURL := cfg.AuthCodeURL("state",
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	fmt.Fprintf(out, "Open this URL to authorize:\n\n%s\n\nWaiting for the callback...\n", authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	tok, err := cfg.Exchange(ctx, code)
	if err != nil {
		return nil, fmt.Errorf("exchange authorization code: %w", err)
	}
	if err := SaveToken(tokenFile, tok); err != nil {
		return nil, err
	}
	return tok, nil
}

// TokenSource returns a source that refreshes the stored token as needed.
func TokenSource(ctx context.Context, credentialsFile, tokenFile string) (oauth2.TokenSource, error) {
	cfg, err := credentialsConfig(credentialsFile)
	if err != nil {
		return nil, err
	}
	tok, err := LoadToken(tokenFile)
	if err != nil {
		return nil, err
	}
	return cfg.TokenSource(ctx, tok), nil
}

// CheckToken forces a token refresh so an expired or revoked token surfaces
// before any mailbox work begins.
func CheckToken(ctx context.Context, credentialsFile, tokenFile string) error {
	ts, err := TokenSource(ctx, credentialsFile, tokenFile)
	if err != nil {
		return err
	}
	return checkTokenSource(ts)
}

func checkTokenSource(ts oauth2.TokenSource) error {
	if _, err := ts.Token(); err != nil {
		if isInvalidGrant(err) {
			return fmt.Errorf("%w: %v", ErrTokenExpired, err)
		}
		return fmt.Errorf("refresh gmail token: %w", err)
	}
	return nil
}

// isInvalidGrant recognises the Google error meaning the refresh token is dead.
func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	if errors.As(err, &re) {
		if re.ErrorCode == "invalid_grant" {
			return true
		}
		if strings.Contains(re.ErrorDescription, "invalid_grant") {
			return true
		}
	}
	return strings.Contains(err.Error(), "invalid_grant")
}

// SaveToken writes the token with owner-only permissions.
func SaveToken(path string, tok *oauth2.Token) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create token directory: %w", err)
		}
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("write token %s: %w", path, err)
	}
	// WriteFile does not change the mode of an existing file.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("set token permissions: %w", err)
	}
	return nil
}

// LoadToken reads a token previously written by SaveToken.
func LoadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read token %s: %w", path, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, fmt.Errorf("parse token %s: %w", path, err)
	}
	return &tok, nil
}
