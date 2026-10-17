package web

import (
	"net/http"
	"time"

	"github.com/paisans-software/paisans-account-service/internal/flow"
)

// registerFormLifetime is how long a rendered registration form may be sent.
const registerFormLifetime = 2 * time.Hour

type registerToken struct {
	N   string
	Exp int64
}

// newRegisterCSRF is the registration form's CSRF value: a signed, expiring
// token, since the person has neither a session nor an invite to bind it to.
func newRegisterCSRF(key []byte) string {
	v, _ := sign(key, "register", registerToken{N: randomString(), Exp: time.Now().Add(registerFormLifetime).Unix()})
	return v
}

func validRegisterCSRF(key []byte, v string) bool {
	var t registerToken
	return verify(key, "register", v, &t) && t.N != "" && time.Now().Unix() < t.Exp
}

func (s *server) registerView(status int, w http.ResponseWriter, errText string, d inviteData) {
	d.Open, d.CoC = true, s.Flow.CoC().HTML
	s.Render.page(w, status, "invite.html", view{Title: "Join", CSRF: newRegisterCSRF(s.SessionKey), Error: errText, Data: d})
}

func (s *server) registerPage(w http.ResponseWriter, r *http.Request) {
	s.registerView(http.StatusOK, w, "", inviteData{})
}

func (s *server) registerSubmit(w http.ResponseWriter, r *http.Request) {
	if !validRegisterCSRF(s.SessionKey, r.PostFormValue("csrf")) {
		http.Error(w, "This form has expired. Go back, reload the page and try again.", http.StatusForbidden)
		return
	}
	a := flow.Applicant{
		Username: r.PostFormValue("username"), Email: r.PostFormValue("email"),
		DisplayName: r.PostFormValue("display_name"),
		FirstName:   r.PostFormValue("first_name"), LastName: r.PostFormValue("last_name"), Agreed: r.PostFormValue("agree") == "yes",
	}
	now, err := s.Flow.Register(r.Context(), a)
	switch {
	case validationSentence(err) != "":
		s.registerView(http.StatusUnprocessableEntity, w, capitalise(validationSentence(err)),
			inviteData{Username: a.Username, DisplayName: a.DisplayName, Email: a.Email,
				FirstName: a.FirstName, LastName: a.LastName})
	case err != nil:
		s.fail(w, err)
	default:
		s.submitted(w, now)
	}
}
