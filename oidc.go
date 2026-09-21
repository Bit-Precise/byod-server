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

type OIDCIdentity struct {
	Issuer        string
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
	Name          string `json:"name"`
	Nickname      string `json:"nickname"`
	Picture       string `json:"picture"`
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
	// Connect's discovery document advertises `openid` (and offline scopes),
	// but not the optional `profile` scope. Request only the declared scope so
	// strict providers do not reject the authorization request with
	// `invalid_scope`.
	auth.OAuth2 = oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: provider.Endpoint(), RedirectURL: redirectURL,
		Scopes: []string{oidc.ScopeOpenID}}
	auth.Verifier = provider.Verifier(&oidc.Config{ClientID: clientID})
	var discovery struct {
		Scopes []string `json:"scopes_supported"`
	}
	if provider.Claims(&discovery) == nil {
		for _, scope := range discovery.Scopes {
			if scope == "email" || scope == "profile" {
				auth.OAuth2.Scopes = append(auth.OAuth2.Scopes, scope)
			}
		}
	}
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
	identity, err := a.exchangeIdentity(ctx, code, verifier, "")
	return identity.Subject, err
}

func (a *OIDCAuthenticator) exchangeIdentity(ctx context.Context, code, verifier, nonce string) (OIDCIdentity, error) {
	var identity OIDCIdentity
	token, err := a.OAuth2.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		return identity, err
	}
	raw, ok := token.Extra("id_token").(string)
	if !ok || raw == "" {
		return identity, errors.New("OIDC response did not contain id_token")
	}
	idToken, err := a.Verifier.Verify(ctx, raw)
	if err != nil {
		return identity, err
	}
	if nonce != "" && idToken.Nonce != nonce {
		return identity, errors.New("OIDC nonce mismatch")
	}
	if err := idToken.Claims(&identity); err != nil || identity.Subject == "" {
		return identity, errors.New("OIDC id_token has no subject")
	}
	identity.Issuer = idToken.Issuer
	// Some providers expose email only on UserInfo. Never trust its claims
	// unless its subject matches the verified ID token.
	if !identity.EmailVerified || identity.Email == "" || identity.Nickname == "" || identity.Picture == "" {
		if info, err := a.Provider.UserInfo(ctx, oauth2.StaticTokenSource(token)); err == nil && info.Subject == identity.Subject {
			if info.EmailVerified {
				identity.Email, identity.EmailVerified = info.Email, true
			}
			var profile struct {
				Name     string `json:"name"`
				Nickname string `json:"nickname"`
				Picture  string `json:"picture"`
			}
			if info.Claims(&profile) == nil {
				if identity.Name == "" {
					identity.Name = profile.Name
				}
				if identity.Nickname == "" {
					identity.Nickname = profile.Nickname
				}
				if identity.Picture == "" {
					identity.Picture = profile.Picture
				}
			}
		}
	}
	return identity, nil
}

// cryptoRandRead is a variable to keep unit tests independent of a global
// random source while using crypto/rand in production.
var cryptoRandRead = func(buffer []byte) (int, error) {
	return cryptoRand.Read(buffer)
}
