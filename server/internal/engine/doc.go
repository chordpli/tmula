// Package engine executes scenario graphs: virtual users walk nodes by
// transition probability while dependency edges stay inviolable (no required
// predecessor is ever skipped). Probabilistic deviation and payload mutation
// are layered on top without violating those edges.
package engine
