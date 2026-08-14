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
