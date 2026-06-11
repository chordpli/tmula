package demo

import _ "embed"

// AccessLog is the demo's traffic sample: a byte-for-byte copy of
// examples/imports/shop-access.log, embedded so `tmula demo` can learn a
// behavior graph from real-looking traffic without any file on disk. go:embed
// cannot reach above the package directory, so the package carries its own
// copy; TestAccessLogStaysInSyncWithExamples keeps it from drifting away from
// the canonical example file.
//
//go:embed shop-access.log
var AccessLog []byte
