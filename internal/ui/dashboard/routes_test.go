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
				}
			}
		})
	}
	slices.Sort(tokenFree)
	if !slices.Equal(tokenFree, []string{"/", healthzPath}) {
		t.Fatalf("token-free routes = %v, want / and /healthz only", tokenFree)
	}
}
