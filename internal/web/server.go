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
	// AccountHost is the host every member and admin route is registered
	// on, and JoinHost the one the signup routes are, lowercase and without
	// a port.
	AccountHost, JoinHost string
	// Signup is config.SignupInvite or config.SignupOpen: it chooses the
	// signup routes, and only invite registers the invite routes.
	Signup string
}

type server struct {
	Options
	// limit guards the signup routes, codeLimit the address-change routes,
	// each per client address.
	limit, codeLimit *limiter
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func New(ctx context.Context, cfg config.Config, f *flow.Service, health pinger) (http.Handler, error) {
	s := sessions{key: cfg.SessionKey, secure: strings.HasPrefix(cfg.AccountURL, "https://")}
	a, err := newAuthenticator(ctx, cfg, s)
	if err != nil {
		return nil, err
	}
	auth := http.NewServeMux()
	auth.HandleFunc("GET /auth/login", a.login)
	auth.HandleFunc("GET /auth/callback", a.callback)
	r, err := newRenderer(cfg.AccentColor, cfg.IDPPublicURL())
	if err != nil {
		return nil, err
	}
	return NewServer(Options{Flow: f, Sessions: s, Auth: auth, Render: r, Health: health, TrustForwarded: cfg.TrustForwarded, SessionKey: cfg.SessionKey,
		AccountHost: cfg.AccountHost(), JoinHost: cfg.JoinHost(), Signup: cfg.Signup}), nil
}

// NewServer registers each route on the host it belongs to, so a request
// for a route on the other host matches nothing and gets a 404. /healthz
// alone has no host: health checks reach the app by its mesh address.
func NewServer(o Options) http.Handler {
	s := &server{Options: o, limit: newLimiter(), codeLimit: newLimiter()}
	mux := http.NewServeMux()
	static, _ := fs.Sub(staticFS, "static")
	files := http.StripPrefix("/static/", http.FileServerFS(static))
	a, j := o.AccountHost, o.JoinHost
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.Handle("GET "+a+"/static/", files)
	mux.Handle("GET "+a+"/auth/", o.Auth)
	mux.HandleFunc("POST "+a+"/auth/logout", s.member(s.logout))
	mux.HandleFunc("GET "+a+"/{$}", s.member(s.home))
	mux.HandleFunc("GET "+a+"/account/email", s.member(s.emailPage))
	mux.HandleFunc("POST "+a+"/account/email", s.limitedBy(s.codeLimit, s.member(s.emailRequest)))
	mux.HandleFunc("POST "+a+"/account/email/confirm", s.limitedBy(s.codeLimit, s.member(s.emailConfirm)))
	mux.HandleFunc("GET "+a+"/account/name", s.member(s.namePage))
	mux.HandleFunc("POST "+a+"/account/name", s.member(s.nameSubmit))
	mux.HandleFunc("GET "+a+"/admin", s.admin(s.queue))
	mux.HandleFunc("POST "+a+"/admin/applications/{id}/approve", s.admin(s.approve))
	mux.HandleFunc("POST "+a+"/admin/applications/{id}/decline", s.admin(s.decline))
	mux.HandleFunc("POST "+a+"/admin/applications/{id}/dismiss", s.admin(s.dismiss))
	mux.HandleFunc("GET "+a+"/admin/members", s.admin(s.members))
	mux.HandleFunc("GET "+a+"/admin/members/{id}/ban", s.admin(s.banPage))
	mux.HandleFunc("POST "+a+"/admin/members/{id}/ban", s.admin(s.banSubmit))
	mux.HandleFunc("GET "+a+"/admin/members/{id}/groups", s.admin(s.groupsPage))
	mux.HandleFunc("POST "+a+"/admin/members/{id}/groups", s.admin(s.groupsSubmit))
	mux.HandleFunc("GET "+a+"/admin/bans", s.admin(s.bans))
	mux.HandleFunc("POST "+a+"/admin/bans/{id}/lift", s.admin(s.lift))
	mux.HandleFunc("GET "+a+"/admin/recovery", s.admin(s.recoveryPage))
	mux.HandleFunc("POST "+a+"/admin/recovery", s.admin(s.recoveryOpen))
	mux.HandleFunc("POST "+a+"/admin/recovery/{id}/approve", s.admin(s.recoveryApprove))
	mux.HandleFunc("POST "+a+"/admin/recovery/{id}/cancel", s.admin(s.recoveryCancel))
	mux.Handle("GET "+j+"/static/", files)
	switch o.Signup {
	case config.SignupInvite:
		mux.HandleFunc("POST "+a+"/invites", s.member(s.createInvite))
		mux.HandleFunc("POST "+a+"/invites/{id}/revoke", s.member(s.revoke))
		mux.HandleFunc("GET "+j+"/i/{token}", s.limited(s.invitePage))
		mux.HandleFunc("POST "+j+"/i/{token}", s.limited(s.inviteSubmit))
	case config.SignupOpen:
		mux.HandleFunc("GET "+j+"/{$}", s.limited(s.registerPage))
		mux.HandleFunc("POST "+j+"/{$}", s.limited(s.registerSubmit))
	}
	return securityHeaders(lowerHost(mux))
}

// lowerHost lowercases the request's host before routing, because ServeMux
// matches a pattern's host exactly and browsers and proxies may send any
// case. ServeMux itself drops the port before it matches.
func lowerHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = strings.ToLower(r.Host)
		next.ServeHTTP(w, r)
	})
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
		// The admin flag the session signed in with goes stale the moment
		// an admin changes the user's groups, so it is read afresh.
		admin, err := s.Flow.IsAdmin(r.Context(), sess.Sub)
		if err != nil {
			s.fail(w, err)
			return
		}
		sess.Admin = admin
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

func (s *server) limited(h http.HandlerFunc) http.HandlerFunc { return s.limitedBy(s.limit, h) }

// limitedBy answers 429 to a client address l has seen too often.
func (s *server) limitedBy(l *limiter, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.allow(limitKey(clientIP(r, s.TrustForwarded))) {
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
