package auth

import "context"

type ctxKey struct{}

const (
	KindGitHub = "github"
	KindToken  = "token"
)

// Actor is who authenticated to sprout-server. No roles — identity only.
type Actor struct {
	Kind  string `json:"kind"`
	Login string `json:"login"`
	ID    int64  `json:"id,omitempty"`
}

func WithActor(ctx context.Context, a Actor) context.Context {
	return context.WithValue(ctx, ctxKey{}, a)
}

func ActorFrom(ctx context.Context) Actor {
	a, _ := ctx.Value(ctxKey{}).(Actor)
	return a
}

// OwnerFrom is the GitHub login for user-scoped rows. Empty for the machine token.
func OwnerFrom(ctx context.Context) string {
	a := ActorFrom(ctx)
	if a.Kind == KindGitHub {
		return a.Login
	}
	return ""
}

// IsUser is true when the caller signed in with GitHub (not SPROUT_TOKEN).
func IsUser(ctx context.Context) bool {
	return ActorFrom(ctx).Kind == KindGitHub
}
