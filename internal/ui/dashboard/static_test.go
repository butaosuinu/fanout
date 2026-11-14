package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticSPAFallback(t *testing.T) {
	const index = "<!doctype html><title>fanout dashboard</title>"
	built := fstest.MapFS{
		"index.html":    {Data: []byte(index)},
		"assets/app.js": {Data: []byte("app bundle")},
		"LICENSE":       {Data: []byte("license text")},
	}
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{
		{"/", http.StatusOK, index},
		{"/sessions/123", http.StatusOK, index},
		{"/sessions/123/", http.StatusOK, index},
		{"/sessions/123?token=secret", http.StatusOK, index},
		{"/project.name/session", http.StatusOK, index},
		{"/assets/app.js", http.StatusOK, "app bundle"},
		{"/LICENSE", http.StatusOK, "license text"},
		{"/missing.js", http.StatusNotFound, "404 page not found\n"},
		{"/missing.js/", http.StatusNotFound, "404 page not found\n"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(method+" "+tc.path, func(t *testing.T) {
				s := &Server{}
				r := httptest.NewRequest(method, tc.path, nil)
				originalURL := r.URL.String()
				w := httptest.NewRecorder()
				s.staticHandler(built)(w, r)
				if w.Code != tc.status {
					t.Fatalf("status = %d, want %d", w.Code, tc.status)
				}
				if tc.status == http.StatusOK && w.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("Cache-Control = %q, want no-store", w.Header().Get("Cache-Control"))
				}
				if method == http.MethodGet && w.Body.String() != tc.body {
					t.Errorf("body = %q, want %q", w.Body.String(), tc.body)
				}
				if method == http.MethodHead && tc.status == http.StatusOK && w.Body.Len() != 0 {
					t.Errorf("HEAD returned a body: %q", w.Body.String())
				}
				if r.URL.String() != originalURL {
					t.Errorf("request URL changed from %q to %q", originalURL, r.URL.String())
				}
			})
		}
	}
}

func TestStaticUnbuiltDeepLinksStayNotFound(t *testing.T) {
	s := &Server{}
	h := s.staticHandler(fstest.MapFS{".gitkeep": {}})
	for _, path := range []string{"/", "/sessions/123", "/sessions/123/", "/.gitkeep", "/assets/app.js"} {
		w := httptest.NewRecorder()
		h(w, httptest.NewRequest(http.MethodGet, path, nil))
		if path == "/" {
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "make build-web") {
				t.Fatalf("root did not serve the build hint: %d %s", w.Code, w.Body.String())
			}
		} else if w.Code != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, w.Code)
		}
	}
}
