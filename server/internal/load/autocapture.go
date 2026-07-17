package load

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// DetectCredential is a best-effort auto-detector for the token (and subject) in a
// login or signup response, so an author does NOT have to hand-write an explicit
// capture path for the common shapes. It is a FALLBACK: callers reach for it only
// when no explicit capture is configured, and treat an empty result as "could not
// detect" (the caller decides whether that is an error).
//
// It searches the response JSON for the first non-empty string value whose key
// matches a ranked token-key list (and a ranked subject-key list), case-insensitive,
// walking nested objects up to maxDetectDepth. When the body carries no recognizable
// token it falls back to a credential-shaped Set-Cookie (session/token/jwt/auth/sid).
// An explicit body token always beats a cookie. It reuses load's dotted-path
// convention (lookupJSONPath) for the nested ranked paths so detection and explicit
// extraction agree on what a path means.
//
// AD-011: the returned token is a secret. DetectCredential never logs it, and the
// caller folds it into a domain.Credential (whose Secret is json:"-").
func DetectCredential(body []byte, setCookie []string) (token, subject string) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		// Not JSON (an HTML error page, a bare cookie response): the body yields
		// nothing, but a Set-Cookie may still carry a session token.
		return tokenFromCookies(setCookie), ""
	}
	token = detectToken(root)
	subject = detectByKeys(root, subjectKeys, subjectPaths)
	if token == "" {
		token = tokenFromCookies(setCookie)
	}
	return token, subject
}

// DetectRefresh is the sibling auto-detector for the OAuth2 refresh grant data in
// a login response: the refresh_token and the access token's lifetime (expires_in,
// in seconds). It is the data foundation for a later real grant_type=refresh_token
// transport — a caller folds the results into a domain.Credential's Refresh /
// ExpiresIn next to the access token DetectCredential already found.
//
// Like DetectCredential it is a best-effort FALLBACK: it searches the response JSON
// for the ranked refresh-key list (refresh_token / refreshToken / …) and the ranked
// expiry-key list (expires_in / expiresIn / …), case-insensitive, walking nested
// objects up to maxDetectDepth, reusing the same key-walk so the two detectors agree
// on shape. Unlike DetectCredential there is NO cookie fallback: a refresh token is
// carried in the body, never as a session cookie. When the body carries neither
// field (or is not JSON) it returns ("", 0), so the caller folds in nothing and the
// credential is byte-for-byte the pre-refresh mint.
//
// AD-011: the refresh token is a secret. DetectRefresh never logs it; the caller
// folds it into a domain.Credential (whose Refresh is json:"-").
func DetectRefresh(body []byte) (refresh string, expiresIn time.Duration) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		// Not JSON: no refresh grant data to recover (no cookie fallback by design).
		return "", 0
	}
	refresh = detectByKeys(root, refreshKeys, refreshPaths)
	if secs, ok := firstNumberByKey(root, expiresInKeys, maxDetectDepth); ok && secs > 0 {
		// expires_in is seconds; keep sub-second precision so a fractional value does
		// not truncate to zero.
		expiresIn = time.Duration(secs * float64(time.Second))
	}
	return refresh, expiresIn
}

// maxDetectDepth bounds how deep the nested object scan descends, so a pathological
// response cannot make detection walk an unbounded tree.
const maxDetectDepth = 3

// tokenKeys is the ranked list of top-level (case-insensitive) keys whose string
// value is taken as the token. Earlier entries win, so a standard OAuth2
// access_token outranks a generic token.
var tokenKeys = []string{
	"access_token",
	"accessToken",
	"token",
	"id_token",
	"idToken",
	"jwt",
	"access",
	"authToken",
	"auth_token",
	"session_token",
	"sessionToken",
}

// tokenPaths is the ranked list of nested dotted paths tried after the flat keys,
// for the common data/result envelope shapes. They use load's lookupJSONPath
// convention so detection agrees with explicit extraction on path meaning.
var tokenPaths = []string{
	"data.token",
	"data.access_token",
	"data.accessToken",
	"result.token",
}

// refreshKeys is the ranked list of (case-insensitive) keys whose string value is
// taken as the OAuth2 refresh token. The standard refresh_token outranks variants.
var refreshKeys = []string{
	"refresh_token",
	"refreshToken",
	"refresh",
}

// refreshPaths is the ranked list of nested dotted refresh paths tried after the
// flat keys, for the common data/result envelope shapes (mirrors tokenPaths).
var refreshPaths = []string{
	"data.refresh_token",
	"data.refreshToken",
	"result.refresh_token",
}

// expiresInKeys is the ranked list of (case-insensitive) keys whose NUMBER value is
// taken as the access token's lifetime in seconds (the OAuth2 expires_in field).
var expiresInKeys = []string{
	"expires_in",
	"expiresIn",
}

// subjectKeys is the ranked list of (case-insensitive) keys whose string value is
// taken as the non-sensitive subject (a principal id for evidence/teardown).
var subjectKeys = []string{
	"username",
	"user_name",
	"userName",
	"sub",
	"email",
	"userId",
	"user_id",
	"id",
	"name",
	"login",
}

// subjectPaths is the ranked list of nested dotted subject paths tried before the
// flat keys, so a user.id envelope resolves to the inner id rather than a top-level
// one.
var subjectPaths = []string{
	"user.id",
}

// detectToken finds the token by the ranked flat keys (deep, case-insensitive)
// first, then the ranked nested paths.
func detectToken(root any) string {
	return detectByKeys(root, tokenKeys, tokenPaths)
}

// detectByKeys runs the shared ranked lookup: for each key in order, the first
// non-empty string value found anywhere in the tree (up to maxDetectDepth) wins;
// if no flat key matches, the ranked dotted paths are tried in order. The flat
// keys are deep, so a nested subject ("id" inside a user object) is reached by the
// "id" key without needing an explicit path; the path list covers only shapes a
// flat key would mis-resolve.
func detectByKeys(root any, keys, paths []string) string {
	for _, key := range keys {
		if v := firstStringByKey(root, key, maxDetectDepth); v != "" {
			return v
		}
	}
	for _, p := range paths {
		if v := stringAtPath(root, p); v != "" {
			return v
		}
	}
	return ""
}

// firstStringByKey returns the first non-empty string value reachable from node
// whose map key equals want (case-insensitive), searching this object and its
// nested objects/arrays breadth-unaware up to depth levels. depth<=0 stops descent.
func firstStringByKey(node any, want string, depth int) string {
	switch n := node.(type) {
	case map[string]any:
		// Prefer a direct key match at this level before descending, so the shallowest
		// match wins (a top-level access_token beats a nested one).
		for k, v := range n {
			if strings.EqualFold(k, want) {
				if s, ok := v.(string); ok && s != "" {
					return s
				}
			}
		}
		if depth <= 1 {
			return ""
		}
		for _, v := range n {
			if s := firstStringByKey(v, want, depth-1); s != "" {
				return s
			}
		}
	case []any:
		if depth <= 1 {
			return ""
		}
		for _, v := range n {
			if s := firstStringByKey(v, want, depth-1); s != "" {
				return s
			}
		}
	}
	return ""
}

// firstNumberByKey runs the ranked numeric lookup: for each key in order, the first
// JSON number reachable from root (case-insensitive, shallowest-first, up to depth
// levels) wins. It mirrors firstStringByKey but for the numeric expires_in field;
// json.Unmarshal into any decodes every JSON number as float64, so the caller gets
// seconds (possibly fractional). ok is false when no key yields a number.
func firstNumberByKey(root any, keys []string, depth int) (n float64, ok bool) {
	for _, key := range keys {
		if v, found := firstFloatByKey(root, key, depth); found {
			return v, true
		}
	}
	return 0, false
}

// firstFloatByKey returns the first JSON number reachable from node whose map key
// equals want (case-insensitive), preferring a direct match at this level before
// descending so the shallowest match wins (mirrors firstStringByKey).
func firstFloatByKey(node any, want string, depth int) (float64, bool) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if strings.EqualFold(k, want) {
				if f, ok := v.(float64); ok {
					return f, true
				}
			}
		}
		if depth <= 1 {
			return 0, false
		}
		for _, v := range n {
			if f, ok := firstFloatByKey(v, want, depth-1); ok {
				return f, true
			}
		}
	case []any:
		if depth <= 1 {
			return 0, false
		}
		for _, v := range n {
			if f, ok := firstFloatByKey(v, want, depth-1); ok {
				return f, true
			}
		}
	}
	return 0, false
}

// stringAtPath resolves a dotted path against root (via load's lookupJSONPath) and
// returns the value as a string only when it is a non-empty JSON string. A missing
// path or a non-string value yields "".
func stringAtPath(root any, path string) string {
	v, err := lookupJSONPath(root, path)
	if err != nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// cookieTokenNames is the set of (lowercased) cookie names treated as carrying a
// session/bearer credential. A Set-Cookie whose name contains one of these is taken
// as the token when the body carries none.
var cookieTokenNames = []string{"session", "token", "jwt", "auth", "sid"}

// tokenFromCookies scans Set-Cookie header values for the first credential-shaped
// cookie (name containing session/token/jwt/auth/sid) and returns its value. It
// parses only the name=value pair, ignoring attributes (Path, HttpOnly, …).
func tokenFromCookies(setCookie []string) string {
	for _, raw := range setCookie {
		name, value := parseCookieNameValue(raw)
		if value == "" {
			continue
		}
		lower := strings.ToLower(name)
		for _, want := range cookieTokenNames {
			if strings.Contains(lower, want) {
				return value
			}
		}
	}
	return ""
}

// parseCookieNameValue extracts the name and value from a single Set-Cookie header
// value, dropping the attributes after the first ';'. It uses net/http's own cookie
// parser so quoting and trimming match how a browser would read the cookie.
func parseCookieNameValue(raw string) (name, value string) {
	header := http.Header{}
	header.Add("Set-Cookie", raw)
	resp := http.Response{Header: header}
	for _, c := range resp.Cookies() {
		return c.Name, c.Value
	}
	// Fall back to a manual split if the stdlib rejected the cookie (e.g. a name it
	// considers invalid) so a slightly off-spec Set-Cookie still yields a token.
	pair := raw
	if i := strings.IndexByte(pair, ';'); i >= 0 {
		pair = pair[:i]
	}
	if eq := strings.IndexByte(pair, '='); eq >= 0 {
		return strings.TrimSpace(pair[:eq]), strings.TrimSpace(pair[eq+1:])
	}
	return "", ""
}
