package byodserver

import (
	"context"
	cryptoRand "crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

// OIDCAuthenticator contains the server-side authorization-code client. No
// access or refresh token is serialized into a browser response.
type OIDCAuthenticator struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Provider     *oidc.Provider
	OAuth2       oauth2.Config
	Verifier     *oidc.IDTokenVerifier
}

func NewOIDCAuthenticator(ctx context.Context, issuer, clientID, clientSecret, redirectURL string) (*OIDCAuthenticator, error) {
	if issuer == "" || clientID == "" || redirectURL == "" {
		return nil, errors.New("OIDC issuer, client ID, and redirect URL are required")
	}
	provider, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	auth := &OIDCAuthenticator{Issuer: issuer, ClientID: clientID, ClientSecret: clientSecret,
		RedirectURL: redirectURL, Provider: provider}
	auth.OAuth2 = oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: provider.Endpoint(), RedirectURL: redirectURL,
		Scopes: []string{oidc.ScopeOpenID, "profile"}}
	auth.Verifier = provider.Verifier(&oidc.Config{ClientID: clientID})
	return auth, nil
}

func pkceVerifier() string {
	buffer := make([]byte, 32)
	if _, err := cryptoRandRead(buffer); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer)
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (a *OIDCAuthenticator) authorizationURL(state, verifier string) string {
	return a.OAuth2.AuthCodeURL(state, oauth2.SetAuthURLParam("code_challenge", pkceChallenge(verifier)), oauth2.SetAuthURLParam("code_challenge_method", "S256"))
}

func (a *OIDCAuthenticator) exchange(ctx context.Context, code, verifier string) (string, error) {
	token, err := a.OAuth2.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return "", err
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return "", errors.New("OIDC response did not contain id_token")
	}
	idToken, err := a.Verifier.Verify(ctx, raw)
	if err != nil {
		return "", err
	}
	var claims struct {
		Subject string `json:"sub"`
	}
	if err := idToken.Claims(&claims); err != nil || claims.Subject == "" {
		return "", errors.New("OIDC id_token has no subject")
	}
	return claims.Subject, nil
}

// cryptoRandRead is a variable to keep unit tests independent of a global
// random source while using crypto/rand in production.
var cryptoRandRead = func(buffer []byte) (int, error) {
	return cryptoRand.Read(buffer)
}
