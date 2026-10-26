package httpapi

import (
	"net/http"
	"strings"

	"aidrop/internal/config"
)

type corsPolicy struct {
	methods   string
	methodSet map[string]struct{}
	headers   string
	headerSet map[string]struct{}
}

var (
	managementCORS = corsPolicy{
		methods:   "GET, POST, PUT, DELETE",
		methodSet: tokenSet(http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete),
		headers:   "Authorization, Content-Type",
		headerSet: tokenSet("authorization", "content-type"),
	}
	mcpCORS = corsPolicy{
		methods:   "GET, POST",
		methodSet: tokenSet(http.MethodGet, http.MethodPost),
		headers:   "Authorization, Content-Type, MCP-Protocol-Version",
		headerSet: tokenSet("authorization", "content-type", "mcp-protocol-version"),
	}
)

func tokenSet(tokens ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		set[token] = struct{}{}
	}
	return set
}

func (s *server) managementCORS(next http.Handler) http.Handler {
	return s.withCORS(next, managementCORS)
}

func (s *server) mcpCORS(next http.Handler) http.Handler {
	return s.withCORS(next, mcpCORS)
}

func (s *server) withCORS(next http.Handler, policy corsPolicy) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origins := r.Header.Values("Origin")
		if len(origins) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		if !validRequestOrigin(r, s.allowedOrigins) {
			writeError(w, http.StatusForbidden, "invalid_origin", "Origin must be same-origin on loopback or explicitly allowed")
			return
		}

		allowedOrigin, err := config.NormalizeOrigin(origins[0])
		if err != nil {
			writeError(w, http.StatusForbidden, "invalid_origin", "Origin must be a valid HTTP or HTTPS origin")
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
		addVary(w.Header(), "Origin")
		if r.Method != http.MethodOptions || r.Header.Get("Access-Control-Request-Method") == "" {
			next.ServeHTTP(w, r)
			return
		}

		addVary(w.Header(), "Access-Control-Request-Method")
		addVary(w.Header(), "Access-Control-Request-Headers")
		if !allowedCORSMethod(r.Header.Get("Access-Control-Request-Method"), policy.methodSet) || !allowedCORSHeaders(r.Header.Values("Access-Control-Request-Headers"), policy.headerSet) {
			writeError(w, http.StatusForbidden, "invalid_preflight", "CORS preflight requested an unsupported method or header")
			return
		}
		w.Header().Set("Access-Control-Allow-Methods", policy.methods)
		w.Header().Set("Access-Control-Allow-Headers", policy.headers)
		w.Header().Set("Access-Control-Max-Age", "600")
		w.WriteHeader(http.StatusNoContent)
	})
}

func allowedCORSMethod(method string, allowed map[string]struct{}) bool {
	_, ok := allowed[method]
	return ok
}

func allowedCORSHeaders(values []string, allowed map[string]struct{}) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return false
		}
		for _, header := range strings.Split(value, ",") {
			header = strings.ToLower(strings.TrimSpace(header))
			if _, ok := allowed[header]; !ok {
				return false
			}
		}
	}
	return true
}

func addVary(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		for _, token := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(token), value) {
				return
			}
		}
	}
	header.Add("Vary", value)
}
