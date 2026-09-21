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

type appleChannelCreationBudgetRepository interface {
	ClaimAppleChannelCreationAttempt(context.Context, int64, string, time.Time) error
	PauseAppleChannelCreation(context.Context, int64, string, time.Time) error
}

type creationChannelWaitError struct{ cause error }

func (e *creationChannelWaitError) Error() string               { return e.cause.Error() }
func (e *creationChannelWaitError) Unwrap() error               { return e.cause }
func (e *creationChannelWaitError) CreationChannelScoped() bool { return true }
