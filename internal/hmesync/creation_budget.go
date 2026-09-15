package hmesync

import (
	"context"
	"time"
)

// Optional for repository adapters; the production Store always implements it.
type appleCreationBudgetRepository interface {
	ClaimAppleCreationAttempt(context.Context, int64, time.Time) error
	PauseAppleCreation(context.Context, int64, time.Time) error
}

type appleCreationProbeRepository interface {
	ClaimAppleCreationProbe(context.Context, int64, time.Time) error
}
