package services

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"mini-aigw/internal/auth"
	"mini-aigw/internal/config"
)

type OIDCStateData struct {
	State        string
	Nonce        string
	CodeVerifier string
	CreatedAt    time.Time
}

type OIDCService struct {
	cfg          *config.Config
	provider     *oidc.Provider
	verifier     *oidc.IDTokenVerifier
	oauth2Config *oauth2.Config

	mu     sync.Mutex
	states map[string]OIDCStateData
}

func NewOIDCService(ctx context.Context, cfg *config.Config) (*OIDCService, error) {
	if cfg.OIDCIssuer == "" || cfg.OIDCClientID == "" {
		return &OIDCService{
			cfg:    cfg,
			states: make(map[string]OIDCStateData),
		}, nil
	}

	provider, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, fmt.Errorf("failed to init oidc provider: %w", err)
	}

	oidcConfig := &oidc.Config{
		ClientID: cfg.OIDCClientID,
	}
	verifier := provider.Verifier(oidcConfig)

	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	if cfg.OIDCScopes != "" {
		scopes = append(scopes, "groups")
	}

	oauth2Cfg := &oauth2.Config{
		ClientID:     cfg.OIDCClientID,
		ClientSecret: cfg.OIDCClientSecret,
		Endpoint:     provider.Endpoint(),
		RedirectURL:  cfg.OIDCRedirectURI,
		Scopes:       scopes,
	}

	return &OIDCService{
		cfg:          cfg,
		provider:     provider,
		verifier:     verifier,
		oauth2Config: oauth2Cfg,
		states:       make(map[string]OIDCStateData),
	}, nil
}

func (s *OIDCService) IsEnabled() bool {
	return s.provider != nil && s.oauth2Config != nil
}

// GenerateAuthURL creates an authorization URL with PKCE (S256), state, and nonce
func (s *OIDCService) GenerateAuthURL() (authURL string, state string, err error) {
	if !s.IsEnabled() {
		return "", "", fmt.Errorf("OIDC is not configured")
	}

	state, err = auth.GenerateRandomSecret(16)
	if err != nil {
		return "", "", err
	}
	nonce, err := auth.GenerateRandomSecret(16)
	if err != nil {
		return "", "", err
	}
	codeVerifier, err := auth.GenerateRandomSecret(32)
	if err != nil {
		return "", "", err
	}

	// S256 code challenge
	h := sha256.Sum256([]byte(codeVerifier))
	codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

	s.mu.Lock()
	s.states[state] = OIDCStateData{
		State:        state,
		Nonce:        nonce,
		CodeVerifier: codeVerifier,
		CreatedAt:    time.Now(),
	}
	s.mu.Unlock()

	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("nonce", nonce),
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	}

	authURL = s.oauth2Config.AuthCodeURL(state, opts...)
	return authURL, state, nil
}

type OIDCUserInfo struct {
	Sub         string   `json:"sub"`
	Username    string   `json:"preferred_username"`
	Email       string   `json:"email"`
	Name        string   `json:"name"`
	Groups      []string `json:"groups"`
	RawIDToken  string   `json:"-"`
}

func (s *OIDCService) ExchangeCode(ctx context.Context, state, code string) (*OIDCUserInfo, error) {
	s.mu.Lock()
	stateData, ok := s.states[state]
	if ok {
		delete(s.states, state)
	}
	s.mu.Unlock()

	if !ok || time.Since(stateData.CreatedAt) > 10*time.Minute {
		return nil, fmt.Errorf("invalid or expired state parameter")
	}

	// Exchange authorization code with PKCE code_verifier
	opts := []oauth2.AuthCodeOption{
		oauth2.SetAuthURLParam("code_verifier", stateData.CodeVerifier),
	}

	token, err := s.oauth2Config.Exchange(ctx, code, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to exchange token: %w", err)
	}

	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok {
		return nil, fmt.Errorf("missing id_token in token response")
	}

	idToken, err := s.verifier.Verify(ctx, rawIDToken)
	if err != nil {
		return nil, fmt.Errorf("failed to verify id_token: %w", err)
	}

	if idToken.Nonce != stateData.Nonce {
		return nil, fmt.Errorf("nonce mismatch")
	}

	var claims struct {
		Sub               string   `json:"sub"`
		PreferredUsername string   `json:"preferred_username"`
		Email             string   `json:"email"`
		Name              string   `json:"name"`
		Groups            []string `json:"groups"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("failed to parse id_token claims: %w", err)
	}

	uname := claims.PreferredUsername
	if uname == "" {
		uname = claims.Sub
	}

	return &OIDCUserInfo{
		Sub:        claims.Sub,
		Username:   uname,
		Email:      claims.Email,
		Name:       claims.Name,
		Groups:     claims.Groups,
		RawIDToken: rawIDToken,
	}, nil
}

func (s *OIDCService) GetEndSessionURL(rawIDToken, postLogoutRedirectURI string) string {
	if s.cfg.OIDCEndSessionEndpoint == "" {
		return ""
	}
	url := fmt.Sprintf("%s?id_token_hint=%s", s.cfg.OIDCEndSessionEndpoint, rawIDToken)
	if postLogoutRedirectURI != "" {
		url += fmt.Sprintf("&post_logout_redirect_uri=%s", postLogoutRedirectURI)
	}
	return url
}

