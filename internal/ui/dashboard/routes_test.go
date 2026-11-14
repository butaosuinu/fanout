package dashboard

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
)

func TestRouteInvariants(t *testing.T) {
	s := &Server{token: "secret", hostPort: "127.0.0.1:1234"}
	h, err := s.handler()
	if err != nil {
		t.Fatal(err)
	}
	var tokenFree []string
	for _, route := range s.routes(fstest.MapFS{}) {
		if route.tokenFree {
			tokenFree = append(tokenFree, route.pattern)
		}
		t.Run(route.pattern, func(t *testing.T) {
			if route.methods == nil {
				t.Fatal("route has no method gate")
			}
			for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions} {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest(method, route.pattern, nil))
				if w.Code != http.StatusMethodNotAllowed || w.Header().Get("Allow") == "" {
					t.Errorf("%s: status = %d, Allow = %q; want 405 with Allow", method, w.Code, w.Header().Get("Allow"))
				}
				assertJSONError(t, w, "method not allowed")
			}
			if !strings.HasPrefix(route.pattern, "/api/") {
				return
			}
			methods := []string{http.MethodGet, http.MethodHead}
			if route.pattern == mergePath || route.pattern == deleteBranchPath {
				methods = []string{http.MethodPost}
			}
			for _, method := range methods {
				for _, query := range []string{"", "?token=wrong"} {
					r := httptest.NewRequest(method, "http://"+s.hostPort+route.pattern+query, nil)
					r.Header.Set("Content-Type", "application/json")
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != http.StatusForbidden {
						t.Errorf("%s %s: status = %d, want 403", method, query, w.Code)
					}
					assertJSONError(t, w, "forbidden")
				}
			}
		})
	}
	slices.Sort(tokenFree)
	if !slices.Equal(tokenFree, []string{"/", healthzPath}) {
		t.Fatalf("token-free routes = %v, want / and /healthz only", tokenFree)
	}
}

func assertJSONError(t *testing.T, w *httptest.ResponseRecorder, msg string) {
	t.Helper()
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := w.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := decodeAPIError(t, w)["error"]; got != msg {
		t.Errorf("error = %q, want %q", got, msg)
	}
}

func TestWriteErrorEscapesMessage(t *testing.T) {
	w := httptest.NewRecorder()
	w.Header().Set("Allow", "GET, HEAD")
	const msg = "invalid pane \"%5\"\n<script>"
	writeError(w, http.StatusBadRequest, msg)
	assertJSONError(t, w, msg)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
	if got := w.Header().Get("Allow"); got != "GET, HEAD" {
		t.Errorf("Allow = %q, want GET, HEAD", got)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := decodeAPIError(t, w); len(got) != 1 {
		t.Errorf("error body = %v, want only error", got)
	}
}

func TestStreamWithoutFlusherReturnsJSONError(t *testing.T) {
	s := &Server{}
	w := httptest.NewRecorder()
	// Expose only ResponseWriter so the recorder's Flusher is unavailable.
	s.handleStream(struct{ http.ResponseWriter }{w}, httptest.NewRequest(http.MethodGet, "/api/stream", nil))
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	assertJSONError(t, w, "streaming unsupported")
}
