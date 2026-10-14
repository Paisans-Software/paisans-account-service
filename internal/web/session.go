package web

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Session struct {
	Sub, Username string
	Admin         bool
	CSRF          string
	Exp           int64
}

type sessions struct {
	key    []byte
	secure bool
}

// sign binds purpose into the MAC so a value minted for one cookie cannot be
// replayed as another.
func sign(key []byte, purpose string, v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose + "\x00"))
	m.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(m.Sum(nil)), nil
}

func verify(key []byte, purpose, s string, v any) bool {
	payload, mac, ok := strings.Cut(s, ".")
	if !ok {
		return false
	}
	b, err1 := base64.RawURLEncoding.DecodeString(payload)
	got, err2 := base64.RawURLEncoding.DecodeString(mac)
	if err1 != nil || err2 != nil {
		return false
	}
	m := hmac.New(sha256.New, key)
	m.Write([]byte(purpose + "\x00"))
	m.Write(b)
	if !hmac.Equal(got, m.Sum(nil)) {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func (s sessions) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge, HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteLaxMode}
}

func (s sessions) write(w http.ResponseWriter, sess Session) {
	v, err := sign(s.key, "session", sess)
	if err != nil {
		slog.Error("session sign failed", "err", err)
		return
	}
	http.SetCookie(w, s.cookie("account_session", v, int(time.Until(time.Unix(sess.Exp, 0)).Seconds())))
}

func (s sessions) read(r *http.Request) (Session, bool) {
	c, err := r.Cookie("account_session")
	if err != nil {
		return Session{}, false
	}
	var sess Session
	if !verify(s.key, "session", c.Value, &sess) || sess.Sub == "" || sess.CSRF == "" || time.Now().Unix() >= sess.Exp {
		return Session{}, false
	}
	return sess, true
}

func (s sessions) clear(w http.ResponseWriter) {
	http.SetCookie(w, s.cookie("account_session", "", -1))
}

func inviteeCSRF(key, tokenHash []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("invitee-csrf:"))
	m.Write(tokenHash)
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}
