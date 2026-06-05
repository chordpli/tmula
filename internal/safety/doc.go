// Package safety guards every outbound request: target host allowlist
// (dev/staging by default, prod locked), a hard rate cap, and a kill switch
// (always-on manual stop plus opt-in automatic thresholds).
package safety
