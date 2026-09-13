package domain

import "time"

// Background creation and manual probes use independent per-account budgets
// with the same local attempt ceilings.
const (
	AppleCreationHourlyLimit = 25
	// This is the hourly ceiling over 24 hours, not a lower daily quota.
	AppleCreationDailyLimit  = 24 * AppleCreationHourlyLimit
	AppleCreationMinInterval = 2 * time.Minute
)
