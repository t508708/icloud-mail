package apple

import "log/slog"

// LogValue exposes only the non-sensitive diagnostic dimensions in a readable
// structured form. It intentionally omits all session material and identifiers.
func (d WebSessionDiagnostics) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Any("region", d.Region),
		slog.Any("service_region", d.ServiceRegion),
		slog.Int("matching_items", d.MatchingCookies),
		slog.Bool("web_auth_present", d.WebAuthPresent),
		slog.Bool("web_user_present", d.WebUserPresent),
		slog.Bool("hme_active", d.HMEActive),
		slog.Bool("hme_available", d.HMEAvailable),
	)
}
