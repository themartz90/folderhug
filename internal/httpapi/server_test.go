package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/themartz90/folderhug/internal/store"
)

func TestReadinessDependsOnDatabase(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := &Server{Store: db}
	h := app.Handler(http.NotFoundHandler())
	for _, status := range []int{200, 503} {
		if status == 503 {
			db.Close()
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/health/ready", nil))
		if w.Code != status {
			t.Fatalf("readiness status %d, expected %d", w.Code, status)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/health/live", nil))
	if w.Code != 200 {
		t.Fatal("database failure must not make liveness fail")
	}
}

func TestUnknownAPIDoesNotServeFrontend(t *testing.T) {
	app := &Server{}
	h := app.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("API request reached frontend") }))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/missing", nil))
	if w.Code != 404 || w.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("unexpected response: %d %s", w.Code, w.Body.String())
	}
}
