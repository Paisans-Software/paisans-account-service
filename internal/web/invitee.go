package web

import "net/http"

func (s *server) invitePage(w http.ResponseWriter, r *http.Request)   { http.NotFound(w, r) }
func (s *server) inviteSubmit(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
