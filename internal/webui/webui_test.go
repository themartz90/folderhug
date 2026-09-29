package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEmbeddedApp(t *testing.T) {
	handler := Handler()
	for _, tc := range []struct {
		method string
		path   string
		status int
	}{
		{http.MethodGet, "/", http.StatusOK},
		{http.MethodHead, "/", http.StatusOK},
		{http.MethodGet, "/missing.js", http.StatusNotFound},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(tc.method, tc.path, nil))
			if response.Code != tc.status {
				t.Fatalf("status = %d, want %d", response.Code, tc.status)
			}
			if tc.method == http.MethodGet && tc.path == "/" && !strings.Contains(response.Body.String(), "Folderhug") {
				t.Fatal("embedded index is missing the application title")
			}
			if tc.method == http.MethodHead && response.Body.Len() != 0 {
				t.Fatal("HEAD must not return a body")
			}
		})
	}
}
