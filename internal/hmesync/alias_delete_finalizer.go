package hmesync

import "context"

type aliasDeletionFinalizerKey struct{}

// WithAliasDeletionFinalizer replaces the local publication step after Apple
// has confirmed deletion. The Apple request, directory reconciliation, account
// serialization, and rate-limit recovery remain owned by DeleteAliases.
func WithAliasDeletionFinalizer(ctx context.Context, finalizer AliasDeletionFinalizer) context.Context {
	if finalizer == nil {
		return ctx
	}
	return context.WithValue(ctx, aliasDeletionFinalizerKey{}, finalizer)
}

func aliasDeletionFinalizerFromContext(ctx context.Context) AliasDeletionFinalizer {
	finalizer, _ := ctx.Value(aliasDeletionFinalizerKey{}).(AliasDeletionFinalizer)
	return finalizer
}
