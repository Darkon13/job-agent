package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerAuthProtectsAPIWithoutToken(t *testing.T) {
	next := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	handler := BearerAuth("secret-token", next)
	cases := []struct {
		name   string
		path   string
		header string
		status int
	}{
		{name: "valid token", path: "/api/v1/version", header: "Bearer secret-token", status: http.StatusNoContent},
		{name: "missing token", path: "/api/v1/version", status: http.StatusUnauthorized},
		{name: "wrong token", path: "/api/v1/version", header: "Bearer other", status: http.StatusUnauthorized},
		{name: "health without token", path: "/healthz", status: http.StatusNoContent},
		{name: "ready without token", path: "/readyz", status: http.StatusNoContent},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testCase.path, nil)
			if testCase.header != "" {
				request.Header.Set("Authorization", testCase.header)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != testCase.status {
				t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestBearerAuthPassesThroughWithoutToken(t *testing.T) {
	next := http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusNoContent)
	})
	handler := BearerAuth("", next)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/version", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d", response.Code)
	}
}
