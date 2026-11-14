package dashboard

// Keep global ReadTimeout and WriteTimeout unset: whole-request deadlines
// would terminate the long-lived /api/stream SSE response.

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"
)

type middleware func(http.HandlerFunc) http.HandlerFunc

type route struct {
	pattern   string
	methods   middleware
	tokenFree bool
	extra     []middleware
	handler   http.HandlerFunc
}

// routes is the complete token-free surface and method policy. methods runs
// before extra (outermost first), then requireToken, then the handler.
func (s *Server) routes(sub fs.FS) []route {
	return []route{
		{healthzPath, s.getOnly, true, nil, handleHealthz},
		{"/api/snapshot", s.getOnly, false, nil, s.handleSnapshot},
		{"/api/stream", s.getOnly, false, nil, s.handleStream},
		{"/api/peek", s.getOnly, false, nil, s.handlePeek},
		{"/api/plan", s.getOnly, false, nil, s.handlePlan},
		{"/api/diff", noStoreMethods(s.getOnly), false, nil, s.handleDiff},
		{mergePath, noStoreMethods(s.postOnly), false, []middleware{s.sameOriginOnly}, s.handleMerge},
		{deleteBranchPath, noStoreMethods(s.postOnly), false, []middleware{s.sameOriginOnly}, s.handleDeleteBranch},
		{"/", s.getOnly, true, nil, s.staticHandler(sub)},
	}
}

func (s *Server) handler() (http.Handler, error) {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	for _, route := range s.routes(sub) {
		mux.HandleFunc(route.pattern, s.routeHandler(route))
	}
	return mux, nil
}

func (s *Server) routeHandler(route route) http.HandlerFunc {
	handler := route.handler
	if !route.tokenFree {
		handler = s.requireToken(handler)
	}
	for _, wrap := range slices.Backward(route.extra) {
		handler = wrap(handler)
	}
	return route.methods(handler)
}

// noStoreMethods keeps no-store outside the method gate, including 405/403
// refusals. POST methods still run before sameOriginOnly and requireToken.
func noStoreMethods(methods middleware) middleware {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return noStore(methods(next))
	}
}

func handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	// A failed write means the client disconnected.
	_, _ = fmt.Fprintln(w, "ok")
}

// writeError is the shared JSON error response for middleware and read APIs.
func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	// A failed response write means the client went away; nothing to do here.
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// getOnly rejects non-GET/HEAD methods with 405. Every route but the POST
// carve-outs reads only, and this is what keeps that true by construction.
func (s *Server) getOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		next(w, r)
	}
}

// postOnly is getOnly's mirror image, for the mutation routes. Having
// both means every route states its method set outright, so a route added later
// cannot inherit a permissive default from either one.
func (s *Server) postOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			apiError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", "")
			return
		}
		next(w, r)
	}
}

// sameOriginOnly is the browser-evidence gate in front of the POST carve-out.
//
// Host must equal the bound 127.0.0.1:<port> exactly, which is what pins DNS
// rebinding: a rebound name reaches this handler as Host: evil.test:<port>.
// Origin is checked only when present, because non-browser clients omit it and
// the token still gates them; Sec-Fetch-Site is read the same way.
//
// The application/json requirement is the load-bearing one. It is not a
// CORS-simple content type, so a cross-origin fetch is preflighted, and the mux
// answers no OPTIONS route — the browser never sends the POST at all. A form
// post, which cannot set that type, dies here with 415.
func (s *Server) sameOriginOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// An unset hostPort means the Server was assembled without New (tests
		// build one directly). Failing closed keeps the mutation route from
		// being the one path that silently loses its rebinding pin.
		if s.hostPort == "" || !sameAuthority(r.Host, s.hostPort) {
			apiError(w, http.StatusForbidden, "host", "unexpected Host header", "")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" &&
			!sameAuthority(strings.TrimPrefix(origin, "http://"), s.hostPort) {
			apiError(w, http.StatusForbidden, "origin", "cross-origin request refused", "")
			return
		}
		switch r.Header.Get("Sec-Fetch-Site") {
		case "", "same-origin", "none":
		default:
			apiError(w, http.StatusForbidden, "site", "cross-site request refused", "")
			return
		}
		if !jsonContentType(r.Header.Get("Content-Type")) {
			apiError(w, http.StatusUnsupportedMediaType, "unsupported_media_type",
				"Content-Type must be application/json", "")
			return
		}
		next(w, r)
	}
}

// sameAuthority compares host:port with the default HTTP port normalized away.
// Browsers drop :80 from the URL, so `fanout dashboard --port 80` would send
// `Host: 127.0.0.1` against a bound `127.0.0.1:80` and fail every POST while the
// page itself loaded fine.
func sameAuthority(got, want string) bool {
	return canonicalAuthority(got) == canonicalAuthority(want)
}

func canonicalAuthority(authority string) string {
	host, port, err := net.SplitHostPort(authority)
	if err != nil {
		return authority // no port at all: already canonical
	}
	if port == "80" {
		return host
	}
	return authority
}

// jsonContentType accepts application/json with any parameters (charset, most
// often) and nothing else.
func jsonContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)
	return err == nil && mediaType == "application/json"
}

// requireToken gates /api/* with a constant-time token compare when a token is
// configured. EventSource cannot set headers, so the token rides as ?token=.
func (s *Server) requireToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.token != "" {
			got := r.URL.Query().Get("token")
			if subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) != 1 {
				writeError(w, http.StatusForbidden, "forbidden")
				return
			}
		}
		next(w, r)
	}
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method == http.MethodGet {
		s.hub.noteSnapshotRequest(time.Now())
	}
	// A failed response write means the client went away; nothing to do here.
	_, _ = w.Write(s.poller.snapshotJSON())
}
