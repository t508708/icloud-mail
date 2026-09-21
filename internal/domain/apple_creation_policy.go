package domain

import (
	"context"
	"time"
)

// Background creation and manual probes use independent per-account budgets.
const (
	AppleAccountCreationHourlyLimit = 19
	ICloudWebCreationHourlyLimit    = 4
	AppleCreationHourlyLimit        = AppleAccountCreationHourlyLimit + ICloudWebCreationHourlyLimit
	// This is the hourly ceiling over 24 hours, not a lower daily quota.
	AppleCreationDailyLimit  = 24 * AppleCreationHourlyLimit
	AppleCreationMinInterval = 2 * time.Minute

	AppleCreationProbeHourlyLimit  = 25
	AppleCreationProbeDailyLimit   = 24 * AppleCreationProbeHourlyLimit
	AppleCreationRateLimitCooldown = time.Hour
)

type scheduledCreationChannelKey struct{}

// Slot order is derived from the persisted plan, so restart does not reset the
// distribution. Four Web slots are spread among the 23 hourly deadlines.
func WithScheduledCreationSlot(ctx context.Context, slot int) context.Context {
	channel := "apple_account"
	if slot >= 0 && slot < AppleCreationHourlyLimit &&
		(slot+1)*ICloudWebCreationHourlyLimit/AppleCreationHourlyLimit > slot*ICloudWebCreationHourlyLimit/AppleCreationHourlyLimit {
		channel = "icloud_web"
	}
	return context.WithValue(ctx, scheduledCreationChannelKey{}, channel)
}

func ScheduledCreationChannel(ctx context.Context) string {
	channel, _ := ctx.Value(scheduledCreationChannelKey{}).(string)
	return channel
}
