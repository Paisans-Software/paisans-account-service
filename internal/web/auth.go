package web

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"github.com/paisans-software/paisans-invite/internal/config"
)

type authenticator struct {
	verifier            *oidc.IDTokenVerifier
	oauth               oauth2.Config
	memberGrp, adminGrp string
	s                   sessions
}

type loginState struct {
	State, Nonce, Verifier string
	Exp                    int64
}

func randomString() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func newAuthenticator(ctx context.Context, cfg config.Config, s sessions) (*authenticator, error) {
	p, err := oidc.NewProvider(ctx, cfg.OIDCIssuer)
	if err != nil {
		return nil, err
	}
	return &authenticator{
		verifier: p.Verifier(&oidc.Config{ClientID: cfg.OIDCClientID}),
		oauth: oauth2.Config{
			ClientID: cfg.OIDCClientID, ClientSecret: cfg.OIDCClientSecret, Endpoint: p.Endpoint(),
			RedirectURL: cfg.PublicURL + "/auth/callback",
			Scopes:      []string{oidc.ScopeOpenID, "profile", "email", "groups"},
		},
		memberGrp: cfg.MemberGroup, adminGrp: cfg.AdminGroup, s: s,
	}, nil
}

func (a *authenticator) login(w http.ResponseWriter, r *http.Request) {
	st := loginState{State: randomString(), Nonce: randomString(), Verifier: oauth2.GenerateVerifier(), Exp: time.Now().Add(10 * time.Minute).Unix()}
	v, err := sign(a.s.key, "login", st)
	if err != nil {
		slog.Error("login state sign failed", "err", err)
		http.Error(w, "Sign-in failed.", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, a.s.cookie("invite_login", v, 600))
	http.Redirect(w, r, a.oauth.AuthCodeURL(st.State, oidc.Nonce(st.Nonce), oauth2.S256ChallengeOption(st.Verifier)), http.StatusFound)
}

func (a *authenticator) callback(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("invite_login")
	var st loginState
	if err != nil || !verify(a.s.key, "login", c.Value, &st) || time.Now().Unix() > st.Exp || r.URL.Query().Get("state") != st.State {
		http.Error(w, "Sign-in expired. Please try again.", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, a.s.cookie("invite_login", "", -1))
	tok, err := a.oauth.Exchange(r.Context(), r.URL.Query().Get("code"), oauth2.VerifierOption(st.Verifier))
	if err != nil {
		http.Error(w, "Sign-in failed.", http.StatusBadGateway)
		return
	}
	raw, _ := tok.Extra("id_token").(string)
	idt, err := a.verifier.Verify(r.Context(), raw)
	if err != nil || idt.Nonce != st.Nonce {
		http.Error(w, "Sign-in failed.", http.StatusBadGateway)
		return
	}
	var claims struct {
		Sub               string   `json:"sub"`
		PreferredUsername string   `json:"preferred_username"`
		Groups            []string `json:"groups"`
	}
	if err := idt.Claims(&claims); err != nil {
		http.Error(w, "Sign-in failed.", http.StatusBadGateway)
		return
	}
	a.admit(w, r, claims.Sub, claims.PreferredUsername, claims.Groups)
}

// admit starts a session for a verified identity, or refuses one that is in
// neither the member nor the admin group.
func (a *authenticator) admit(w http.ResponseWriter, r *http.Request, sub, username string, groups []string) {
	admin := slices.Contains(groups, a.adminGrp)
	if !admin && !slices.Contains(groups, a.memberGrp) {
		http.Error(w, "Only members can make invites.", http.StatusForbidden)
		return
	}
	a.s.write(w, Session{Sub: sub, Username: username, Admin: admin, CSRF: randomString(), Exp: time.Now().Add(8 * time.Hour).Unix()})
	http.Redirect(w, r, "/", http.StatusFound)
}
