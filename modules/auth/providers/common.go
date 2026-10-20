package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/vernal96/go-cms-kernel/modules/auth"
)

const maxResponseBytes = 1 << 20

var errInvalidResponse = errors.New("invalid identity provider response")

type config struct {
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

func (c config) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return &http.Client{Timeout: 8 * time.Second}
}

func (c config) validate() error {
	if strings.TrimSpace(c.ClientID) == "" {
		return errors.New("client ID is required")
	}
	return nil
}

func validateEndpoint(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") ||
		u.Host == "" || u.User != nil || u.Fragment != "" {
		return errors.New("provider endpoint must be an absolute HTTP URL")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("provider endpoint must use HTTPS outside loopback")
		}
	}
	return nil
}

func authorizationURL(endpoint, clientID, redirectURI string, requestState,
	nonce, challenge string, extra url.Values) (string, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return "", err
	}
	if clientID == "" || requestState == "" || redirectURI == "" {
		return "", errors.New("client ID, state and redirect URI are required")
	}
	redirect, err := url.Parse(redirectURI)
	if err != nil || (redirect.Scheme != "https" && redirect.Scheme != "http") ||
		redirect.Host == "" || redirect.User != nil || redirect.Fragment != "" {
		return "", errors.New("redirect URI must be an absolute HTTP URL")
	}
	parsed, _ := url.Parse(endpoint)
	query := parsed.Query()
	query.Set("client_id", clientID)
	query.Set("redirect_uri", redirectURI)
	query.Set("response_type", "code")
	query.Set("state", requestState)
	if nonce != "" {
		query.Set("nonce", nonce)
	}
	if challenge != "" {
		query.Set("code_challenge", challenge)
		query.Set("code_challenge_method", "S256")
	}
	for key, values := range extra {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func postForm(ctx context.Context, client *http.Client, endpoint string,
	form url.Values, bearer string) (json.RawMessage, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return doJSON(client, req)
}

func getJSON(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	bearer string,
) (json.RawMessage, error) {
	if err := validateEndpoint(endpoint); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	return doJSON(client, req)
}

func doJSON(client *http.Client, req *http.Request) (json.RawMessage, error) {
	requestCtx, cancel := context.WithTimeout(req.Context(), 8*time.Second)
	defer cancel()
	req = req.WithContext(requestCtx)
	boundedClient := *client
	if boundedClient.Timeout == 0 || boundedClient.Timeout > 8*time.Second {
		boundedClient.Timeout = 8 * time.Second
	}
	boundedClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := boundedClient.Do(req)
	if err != nil {
		var urlError *url.Error
		if errors.As(err, &urlError) {
			// URL queries can contain provider credentials or access tokens.
			err = urlError.Err
		}
		return nil, fmt.Errorf("identity provider request failed: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read identity provider response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, errInvalidResponse
	}
	var failure struct {
		Error string `json:"error"`
	}
	if response.StatusCode < http.StatusInternalServerError && json.Unmarshal(body, &failure) == nil {
		switch failure.Error {
		case "invalid_grant", "access_denied", "invalid_token", "interaction_required",
			"login_required", "consent_required":
			return nil, auth.ErrRejected
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("identity provider returned HTTP %d", response.StatusCode)
	}
	if !json.Valid(body) {
		return nil, errInvalidResponse
	}
	return body, nil
}

func decode(raw json.RawMessage, target any) error {
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: %v", errInvalidResponse, err)
	}
	return nil
}

func validateIdentity(subject, email string) error {
	if subject == "" || len(subject) > 512 || strings.TrimSpace(subject) != subject ||
		strings.ContainsAny(subject, "\r\n\x00") {
		return fmt.Errorf("%w: missing or invalid subject", auth.ErrRejected)
	}
	if email != "" {
		parsed, err := mail.ParseAddress(email)
		if err != nil || parsed.Address != email || strings.ContainsAny(email, "\r\n") {
			return fmt.Errorf("%w: invalid email", auth.ErrRejected)
		}
	}
	return nil
}

func requiredExchange(requestCode, redirectURI string) error {
	if requestCode == "" || redirectURI == "" {
		return errors.New("authorization code and redirect URI are required")
	}
	return nil
}
