// Package domain holds the core domain model for the traffic simulator:
// experiments, scenario graphs, virtual users, load profiles, runs and findings.
package domain

import (
	"fmt"
	"strings"
)

// Role is the execution role of a tmula process. The same binary runs as a
// self-contained local engine, or as a master/worker in distributed mode.
type Role string

const (
	RoleLocal  Role = "local"
	RoleMaster Role = "master"
	RoleWorker Role = "worker"
)

// ParseRole validates and normalizes a role string (case/space-insensitive).
func ParseRole(s string) (Role, error) {
	switch Role(strings.ToLower(strings.TrimSpace(s))) {
	case RoleLocal:
		return RoleLocal, nil
	case RoleMaster:
		return RoleMaster, nil
	case RoleWorker:
		return RoleWorker, nil
	default:
		return "", fmt.Errorf("invalid role %q (want local|master|worker)", s)
	}
}
