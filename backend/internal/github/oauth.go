package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Scopes DevLab requests: full repo access (clone/push private+public) and org read (to see
// org-member repos in the holistic set). GitHub is the single source of truth for what the user
// may actually do — these scopes only bound what the token *could* do.
const Scopes = "repo read:org"

// OAuthConfig is the GitHub OAuth App registration. Loaded from DEVLAB_GITHUB_OAUTH_FILE (a
// root-owned 0640 JSON file readable by the devlab group), never from the request.
type OAuthConfig struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	// RedirectURI is the absolute callback on the instance host, e.g.
	// https://devlab.example.org/api/github/callback.
	RedirectURI string `json:"redirect_uri"`
}

// LoadOAuthConfig reads the OAuth app config from DEVLAB_GITHUB_OAUTH_FILE. Returns an error
// (not a partial config) when unset or incomplete, so handlers can 503 cleanly instead of
// starting a broken flow.
func LoadOAuthConfig() (*OAuthConfig, error) {
	path := os.Getenv("DEVLAB_GITHUB_OAUTH_FILE")
	if path == "" {
		return nil, errors.New("github: DEVLAB_GITHUB_OAUTH_FILE not set")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("github: read oauth config: %w", err)
	}
	var c OAuthConfig
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("github: parse oauth config: %w", err)
	}
	if c.ClientID == "" || c.ClientSecret == "" || c.RedirectURI == "" {
		return nil, errors.New("github: oauth config missing client_id/client_secret/redirect_uri")
	}
	return &c, nil
}

// AuthorizeURL builds the GitHub authorization URL the user is redirected to. state is an opaque
// anti-CSRF nonce the caller also stores in a cookie and re-checks at the callback.
func (c *OAuthConfig) AuthorizeURL(state string) string {
	q := url.Values{
		"client_id":    {c.ClientID},
		"redirect_uri": {c.RedirectURI},
		"scope":        {Scopes},
		"state":        {state},
		// We always want a token tied to the acting user; GitHub reuses an existing grant.
		"allow_signup": {"false"},
	}
	return "https://github.com/login/oauth/authorize?" + q.Encode()
}

// Exchange swaps an authorization code for a user access token. Returns the token and the granted
// scope string. The token is a secret — the caller encrypts it at rest and never logs it.
func (c *OAuthConfig) Exchange(ctx context.Context, code string) (token, scope string, err error) {
	form := url.Values{
		"client_id":     {c.ClientID},
		"client_secret": {c.ClientSecret},
		"code":          {code},
		"redirect_uri":  {c.RedirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://github.com/login/oauth/access_token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", userAgent)
	res, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return "", "", fmt.Errorf("github: token exchange %s: %s", res.Status, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken      string `json:"access_token"`
		Scope            string `json:"scope"`
		TokenType        string `json:"token_type"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return "", "", fmt.Errorf("github: decode token: %w", err)
	}
	if out.Error != "" {
		return "", "", fmt.Errorf("github: token exchange: %s (%s)", out.Error, out.ErrorDescription)
	}
	if out.AccessToken == "" {
		return "", "", errors.New("github: token exchange returned no access_token")
	}
	return out.AccessToken, out.Scope, nil
}
