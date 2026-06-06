// Package workload implements the open (arrival-rate driven) traffic model.
//
// Where the closed model (internal/load Runner.Run) spawns a fixed pool of
// users that loop, the open model has users *arrive* over time at a configurable
// rate, each running a single session — walk the graph, call the bound APIs with
// think time between steps, then leave. Concurrency is therefore not a knob but
// an emergent property of arrival rate × session duration (Little's Law), which
// is how real traffic behaves and how it scales.
//
// The rate(t) function (rate.go) turns a domain.ArrivalProfile into an
// arrivals/sec value over time, the open-model analogue of load.NewStrategy. The
// Scheduler (scheduler.go) launches sessions as a Poisson process whose
// instantaneous intensity tracks rate(t), applies a back-pressure cap, and feeds
// every step into the same obs.Collector + obs.Aggregator the closed path uses,
// so findings are identical regardless of model. It reuses load.Runner.RunSession
// for the per-arrival journey rather than duplicating the walk/render/send logic.
package workload
