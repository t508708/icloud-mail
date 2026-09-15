package domain

import "time"

// Background creation and manual probes use independent per-account budgets.
const (
	AppleCreationHourlyLimit = 18
	// This is the hourly ceiling over 24 hours, not a lower daily quota.
	AppleCreationDailyLimit  = 24 * AppleCreationHourlyLimit
	AppleCreationMinInterval = 2 * time.Minute

	AppleCreationProbeHourlyLimit = 25
	AppleCreationProbeDailyLimit  = 24 * AppleCreationProbeHourlyLimit
)
