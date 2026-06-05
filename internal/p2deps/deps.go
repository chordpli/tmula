// Package p2deps pins the distributed-mode dependencies during P2 development.
// It is removed once internal/cluster and internal/store import them directly.
package p2deps

import (
	_ "github.com/jackc/pgx/v5"
	_ "google.golang.org/grpc"
	_ "google.golang.org/protobuf/proto"
)
