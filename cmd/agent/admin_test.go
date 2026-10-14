package main

import (
	"gvs-pr-agent/internal/scheduler"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAdminTriggerRequiresBearerAuthentication(t *testing.T) {
	s := scheduler.New(time.Hour, 0, slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, token := range []string{"", "secret"} {
		h := adminHandler(s, token)
		for _, auth := range []string{"", "Bearer wrong", "Bearer secret"} {
			r := httptest.NewRequest(http.MethodPost, "/trigger", nil)
			r.Header.Set("Authorization", auth)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := http.StatusUnauthorized
			if token == "" {
				want = http.StatusServiceUnavailable
			} else if auth == "Bearer secret" {
				want = http.StatusAccepted
			}
			if w.Code != want {
				t.Fatalf("token configured=%v auth=%s status=%d want=%d", token != "", auth, w.Code, want)
			}
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
		if w.Code != 200 {
			t.Fatal("liveness requires credentials")
		}
	}
}
