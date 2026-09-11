package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// BearerAuth protects the API with a static bearer token. An empty token
// leaves the handler unprotected, which is only valid together with a
// loopback bind. Health endpoints stay open so container orchestration can
// probe the process without holding the token.
func BearerAuth(token string, next http.Handler) http.Handler {
	token = strings.TrimSpace(token)
	if token == "" {
		return next
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/healthz" || request.URL.Path == "/readyz" {
			next.ServeHTTP(response, request)
			return
		}
		provided := []byte(strings.TrimSpace(request.Header.Get("Authorization")))
		if subtle.ConstantTimeCompare(provided, expected) != 1 {
			writeProblem(response, http.StatusUnauthorized, "invalid or missing API token")
			return
		}
		next.ServeHTTP(response, request)
	})
}
