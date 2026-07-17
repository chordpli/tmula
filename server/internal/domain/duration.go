package domain

import (
	"encoding/json"
	"time"
)

// flexDuration is a time.Duration at the JSON boundary that marshals as a Go-duration
// STRING ("1h0m0s") and unmarshals from EITHER that string or a raw nanosecond number.
//
// The duration-bearing auth specs (mint TTL/Leeway, exec Timeout) are authored as
// human duration strings everywhere a human writes them — the scenario-file YAML/JSON
// documents "1h"/"30m"/"5s", and the web console posts the same. But a bare
// time.Duration field only accepts a JSON number (nanoseconds), so a browser- or
// hand-authored spec 400s at decode. Routing those fields through flexDuration makes
// the domain wire contract match the documented string convention while still accepting
// the number form a Go-marshaled spec (e.g. a distributed ShardSpec) produces.
type flexDuration time.Duration

func (d flexDuration) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Duration(d).String())
}

func (d *flexDuration) UnmarshalJSON(b []byte) error {
	if len(b) == 0 || string(b) == "null" {
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		if s == "" {
			*d = 0
			return nil
		}
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return err
		}
		*d = flexDuration(parsed)
		return nil
	}
	var n int64
	if err := json.Unmarshal(b, &n); err != nil {
		return err
	}
	*d = flexDuration(n)
	return nil
}
