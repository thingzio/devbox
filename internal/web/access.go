package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
)

const (
	sessionCookie = "devbox_session"
	tokenParam    = "token"
	accessDenied  = "devbox: open the URL printed in the terminal when devbox started; it carries this server's access token\n"
)

// sessionValue derives the cookie from the access token, so the cookie never
// holds the token itself.
func sessionValue(token string) string {
	sum := sha256.Sum256([]byte("devbox-session\x00" + token))
	return hex.EncodeToString(sum[:])
}

func equal(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// admit enforces the access token when the server accepts remote connections.
// A GET carrying the token sets the session cookie and redirects to the same
// URL without it; every other request needs the cookie. It reports whether
// the request may proceed; otherwise it has already written the response.
func (s *Server) admit(w http.ResponseWriter, r *http.Request) bool {
	if s.session == "" || r.URL.Path == "/healthz" {
		return true
	}
	q := r.URL.Query()
	if token := q.Get(tokenParam); token != "" && r.Method == http.MethodGet {
		if !equal(token, s.o.AccessToken) {
			http.Error(w, accessDenied, http.StatusUnauthorized)
			return false
		}
		// Secure only over TLS: DevBox serves plain HTTP, where a Secure cookie would never be sent back.
		http.SetCookie(w, &http.Cookie{ //nolint:gosec // Secure follows the connection; see above
			Name: sessionCookie, Value: s.session, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil,
		})
		q.Del(tokenParam)
		next := r.URL.Path
		if len(q) > 0 {
			next += "?" + q.Encode()
		}
		http.Redirect(w, r, localPath(next), http.StatusSeeOther) //nolint:gosec // localPath allows only paths on this server
		return false
	}
	if c, err := r.Cookie(sessionCookie); err == nil && equal(c.Value, s.session) {
		return true
	}
	http.Error(w, accessDenied, http.StatusUnauthorized)
	return false
}
