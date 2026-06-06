package domain

import "math"

// EstimatedConcurrency applies Little's Law: the average number of users active
// at once equals the arrival rate (users/sec) times the average time each user
// stays (the session duration, in seconds). This is how a target like "1M users
// per hour" translates into a much smaller concurrent peak.
//
//	EstimatedConcurrency(arrivalPerSec, avgSessionSeconds)
//	  e.g. 1,000,000/hour ≈ 278/s, 60s session → ~16,700 concurrent.
func EstimatedConcurrency(arrivalPerSec, avgSessionSeconds float64) float64 {
	if arrivalPerSec <= 0 || avgSessionSeconds <= 0 {
		return 0
	}
	return arrivalPerSec * avgSessionSeconds
}

// ArrivalRateForTotal converts a "total users over a window" target into the
// steady arrival rate (users/sec) needed to deliver it.
func ArrivalRateForTotal(totalUsers int, windowSeconds float64) float64 {
	if totalUsers <= 0 || windowSeconds <= 0 {
		return 0
	}
	return float64(totalUsers) / windowSeconds
}

// WorkersNeeded returns how many workers are required to sustain a given
// concurrency, assuming each worker comfortably handles perWorkerCap concurrent
// users. Returns 0 when inputs are non-positive (unknown).
func WorkersNeeded(concurrency, perWorkerCap int) int {
	if concurrency <= 0 || perWorkerCap <= 0 {
		return 0
	}
	return int(math.Ceil(float64(concurrency) / float64(perWorkerCap)))
}

// CapacityPlan summarizes what a target population implies for the run.
type CapacityPlan struct {
	TotalUsers        int     `json:"totalUsers"`
	WindowSeconds     float64 `json:"windowSeconds"`
	AvgSessionSeconds float64 `json:"avgSessionSeconds"`
	ArrivalPerSec     float64 `json:"arrivalPerSec"`
	PeakConcurrency   int     `json:"peakConcurrency"`
	WorkersNeeded     int     `json:"workersNeeded"`
}

// PlanCapacity computes a capacity plan from a target population. perWorkerCap is
// the concurrency a single worker can sustain (e.g. ~2000); pass 0 if unknown.
func PlanCapacity(totalUsers int, windowSeconds, avgSessionSeconds float64, perWorkerCap int) CapacityPlan {
	rate := ArrivalRateForTotal(totalUsers, windowSeconds)
	conc := EstimatedConcurrency(rate, avgSessionSeconds)
	peak := int(math.Ceil(conc))
	return CapacityPlan{
		TotalUsers:        totalUsers,
		WindowSeconds:     windowSeconds,
		AvgSessionSeconds: avgSessionSeconds,
		ArrivalPerSec:     rate,
		PeakConcurrency:   peak,
		WorkersNeeded:     WorkersNeeded(peak, perWorkerCap),
	}
}
