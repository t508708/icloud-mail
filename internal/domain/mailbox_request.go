package domain

import "context"

type mailboxRequestIDKey struct{}

func WithMailboxRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, mailboxRequestIDKey{}, id)
}

func MailboxRequestID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(mailboxRequestIDKey{}).(string)
	return id
}
