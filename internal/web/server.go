package web

import (
	"context"
	"crypto/subtle"
	"io/fs"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/paisans-software/paisans-account-service/internal/config"
	"github.com/paisans-software/paisans-account-service/internal/flow"
)

type pinger interface{ Ping(context.Context) error }

type Options struct {
	Flow           *flow.Service
	Sessions       sessions
	Auth           http.Handler
	Render         *renderer
	Health         pinger
	TrustForwarded bool
	SessionKey     []byte
}

type server struct {
	Options
	limit *limiter
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func New(ctx context.Context, cfg config.Config, f *flow.Service, health pinger) (http.Handler, error) {
	s := sessions{key: cfg.SessionKey, secure: strings.HasPrefix(cfg.PublicURL, "https://")}
	a, err := newAuthenticator(ctx, cfg, s)
	if err != nil {
		return nil, err
	}
	auth := http.NewServeMux()
	auth.HandleFunc("GET /auth/login", a.login)
	auth.HandleFunc("GET /auth/callback", a.callback)
	r, err := newRenderer(cfg.AccentColor, cfg.IDPURL)
	if err != nil {
		return nil, err
	}
	return NewServer(Options{Flow: f, Sessions: s, Auth: auth, Render: r, Health: health, TrustForwarded: cfg.TrustForwarded, SessionKey: cfg.SessionKey}), nil
}

func NewServer(o Options) http.Handler {
	s := &server{Options: o, limit: newLimiter()}
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(static)))
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET /auth/", o.Auth)
	mux.HandleFunc("POST /auth/logout", s.member(s.logout))
	mux.HandleFunc("GET /{$}", s.member(s.home))
	mux.HandleFunc("POST /invites", s.member(s.createInvite))
	mux.HandleFunc("POST /invites/{id}/revoke", s.member(s.revoke))
	mux.HandleFunc("GET /admin", s.admin(s.queue))
	mux.HandleFunc("POST /admin/applications/{id}/approve", s.admin(s.approve))
	mux.HandleFunc("POST /admin/applications/{id}/decline", s.admin(s.decline))
	mux.HandleFunc("POST /admin/applications/{id}/dismiss", s.admin(s.dismiss))
	mux.HandleFunc("GET /i/{token}", s.limited(s.invitePage))
	mux.HandleFunc("POST /i/{token}", s.limited(s.inviteSubmit))
	return securityHeaders(mux)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}

func (s *server) healthz(w http.ResponseWriter, r *http.Request) {
	if err := s.Health.Ping(r.Context()); err != nil {
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.Write([]byte("ok"))
}

type sessionHandler func(http.ResponseWriter, *http.Request, Session)

func (s *server) member(h sessionHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.Sessions.read(r)
		if !ok {
			// A GET comes back to where it started; a form post cannot be
			// replayed, so it starts again from the home page.
			login := "/auth/login"
			if r.Method == http.MethodGet {
				login += "?next=" + url.QueryEscape(r.URL.RequestURI())
			}
			http.Redirect(w, r, login, http.StatusFound)
			return
		}
		if r.Method == http.MethodPost && subtle.ConstantTimeCompare([]byte(r.PostFormValue("csrf")), []byte(sess.CSRF)) != 1 {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		h(w, r, sess)
	}
}

func (s *server) admin(h sessionHandler) http.HandlerFunc {
	return s.member(func(w http.ResponseWriter, r *http.Request, sess Session) {
		if !sess.Admin {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		h(w, r, sess)
	})
}

func (s *server) limited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.limit.allow(limitKey(clientIP(r, s.TrustForwarded))) {
			http.Error(w, "Too many requests. Please wait a minute.", http.StatusTooManyRequests)
			return
		}
		h(w, r)
	}
}

func (sess Session) member() flow.Member {
	return flow.Member{Sub: sess.Sub, Username: sess.Username, Admin: sess.Admin}
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, _ Session) {
	s.Sessions.clear(w)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}
