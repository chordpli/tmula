package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/chordpli/tmula/server/internal/safety"
)

// DiscoverRequest asks the server to fetch an OpenID Connect provider's discovery
// document. Issuer is either a bare issuer origin (https://idp.example.com) or a full
// .../.well-known/openid-configuration URL. Allow is the target allowlist the issuer host
// must be in — the SAME allowlist a run/preflight uses — so this server-side fetch cannot
// be turned into an SSRF probe of an arbitrary host.
type DiscoverRequest struct {
	Issuer string   `json:"issuer"`
	Allow  []string `json:"allow,omitempty"`
}

// DiscoverResult is the discovery outcome: on success the resolved issuer, the token
// endpoint the OAuth2/login route should POST to, and (when the document lists them) the
// supported grant types. On a fetch/parse failure OK is false and Reason names what went
// wrong (and the URL tried). An allowlist rejection is a 403, not an ok:false body.
type DiscoverResult struct {
	OK                  bool     `json:"ok"`
	Issuer              string   `json:"issuer,omitempty"`
	TokenEndpoint       string   `json:"tokenEndpoint,omitempty"`
	GrantTypesSupported []string `json:"grantTypesSupported,omitempty"`
	Reason              string   `json:"reason,omitempty"`
}

// discovery-fetch bounds: a short timeout, at most two redirects, and a 1 MiB response
// cap, so a slow/hostile IdP cannot hang the server or exhaust memory.
const (
	discoverTimeout     = 5 * time.Second
	discoverMaxRedirect = 2
	discoverMaxBytes    = 1 << 20
)

// openidConfigSuffix is the well-known path an OpenID Connect discovery document lives at.
const openidConfigSuffix = "/.well-known/openid-configuration"

// authDiscover implements POST /auth/discover: fetch an OpenID Connect provider's
// discovery document and return its token endpoint, so a web "paste your issuer URL →
// token URL auto-filled" flow works without the operator hand-copying it. It is a
// server-side fetch to a USER-SUPPLIED URL, so it is SSRF-gated exactly like login
// traffic: the issuer host must pass the target allowlist (403 otherwise), and every
// redirect hop is re-checked against the same guard.
func (s *Server) authDiscover(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	var req DiscoverRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("decode: %w", err))
		return
	}
	if strings.TrimSpace(req.Issuer) == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("an issuer URL is required"))
		return
	}

	discoveryURL, err := discoveryURLFor(req.Issuer)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	// SSRF gate: build the same allowlist guard a run/preflight uses and require the
	// issuer host to be in it. An empty allowlist is a 400 (the caller must say which host
	// it authorizes); a host outside the allowlist is a 403 with an actionable message.
	guard, err := safety.NewGuard(safety.Config{Allowlist: req.Allow, MaxRPS: 10, MaxConcurrency: 2})
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("provide an allowlist (allow) the issuer host is in: %w", err))
		return
	}
	if err := guard.AllowHost(discoveryURL); err != nil {
		host, _ := hostOfURL(discoveryURL)
		writeErr(w, http.StatusForbidden, fmt.Errorf("add %s to the allowlist to discover it (%v)", host, err))
		return
	}

	writeJSON(w, http.StatusOK, discoverOIDC(r.Context(), guard, discoveryURL))
}

// discoveryURLFor normalizes an issuer into its discovery-document URL: a URL already
// ending in the well-known suffix is used as-is, otherwise the suffix is appended to the
// issuer origin (trimming a trailing slash). It requires an https/http URL with a host, so
// a bare hostname or a non-URL is rejected up front.
func discoveryURLFor(issuer string) (string, error) {
	issuer = strings.TrimSpace(issuer)
	u, err := url.Parse(issuer)
	if err != nil {
		return "", fmt.Errorf("issuer %q is not a valid URL: %w", issuer, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("issuer %q must be an http(s) URL", issuer)
	}
	if u.Host == "" {
		return "", fmt.Errorf("issuer %q has no host", issuer)
	}
	if strings.HasSuffix(strings.TrimRight(u.Path, "/"), openidConfigSuffix) {
		return issuer, nil
	}
	return strings.TrimRight(issuer, "/") + openidConfigSuffix, nil
}

// oidcDiscoveryDoc is the slice of an OpenID Connect discovery document we read.
type oidcDiscoveryDoc struct {
	Issuer              string   `json:"issuer"`
	TokenEndpoint       string   `json:"token_endpoint"`
	GrantTypesSupported []string `json:"grant_types_supported"`
}

// discoverOIDC fetches and parses the discovery document under the fetch bounds, re-checking
// the allowlist on every redirect hop (SSRF-safe). It returns an ok:false result — never an
// error — for any fetch/parse problem, naming the URL tried so the outcome is readable.
func discoverOIDC(ctx context.Context, guard *safety.Guard, discoveryURL string) DiscoverResult {
	client := &http.Client{
		Timeout: discoverTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= discoverMaxRedirect {
				return fmt.Errorf("stopped after %d redirects", discoverMaxRedirect)
			}
			// A redirect could point at a NON-allowlisted host; re-gate every hop so a
			// discovery fetch cannot be redirected into an SSRF.
			return guard.AllowHost(req.URL.String())
		},
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		return DiscoverResult{OK: false, Reason: fmt.Sprintf("build request for %s: %v", discoveryURL, err)}
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return DiscoverResult{OK: false, Reason: fmt.Sprintf("could not reach %s: %v", discoveryURL, err)}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return DiscoverResult{OK: false, Reason: fmt.Sprintf("%s returned status %d (want 200)", discoveryURL, resp.StatusCode)}
	}

	// Read at most the cap + 1 byte so an oversize document is detected, not buffered.
	body, err := io.ReadAll(io.LimitReader(resp.Body, discoverMaxBytes+1))
	if err != nil {
		return DiscoverResult{OK: false, Reason: fmt.Sprintf("read %s: %v", discoveryURL, err)}
	}
	if len(body) > discoverMaxBytes {
		return DiscoverResult{OK: false, Reason: fmt.Sprintf("%s response exceeds the %d-byte discovery-document cap", discoveryURL, discoverMaxBytes)}
	}

	var doc oidcDiscoveryDoc
	if err := json.Unmarshal(body, &doc); err != nil {
		return DiscoverResult{OK: false, Reason: fmt.Sprintf("%s did not return a JSON discovery document: %v", discoveryURL, err)}
	}
	if strings.TrimSpace(doc.TokenEndpoint) == "" {
		return DiscoverResult{OK: false, Reason: fmt.Sprintf("%s returned a discovery document with no token_endpoint", discoveryURL)}
	}
	return DiscoverResult{
		OK:                  true,
		Issuer:              doc.Issuer,
		TokenEndpoint:       doc.TokenEndpoint,
		GrantTypesSupported: doc.GrantTypesSupported,
	}
}

// hostOfURL returns the host (without port) of a URL, for the allowlist message.
func hostOfURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("cannot parse host from %q", raw)
	}
	return u.Hostname(), nil
}
