// Package progress streams long-running control-plane steps to an HTTP client.
package progress

import (
	"context"
	"fmt"
	"os"
	"time"
)

type ctxKey struct{}

type Event struct {
	Type   string `json:"type"` // step | result | error
	Step   string `json:"step,omitempty"`
	Detail string `json:"detail,omitempty"`
	MS     int64  `json:"ms,omitempty"`
}

type Writer func(Event)

func With(ctx context.Context, w Writer) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, ctxKey{}, w)
}

func Log(ctx context.Context, step, detail string) {
	line := step
	if detail != "" {
		line = step + " " + detail
	}
	fmt.Fprintln(os.Stdout, line)
	if w, _ := ctx.Value(ctxKey{}).(Writer); w != nil {
		w(Event{Type: "step", Step: step, Detail: detail})
	}
}

func Println(ctx context.Context, a ...any) {
	s := fmt.Sprint(a...)
	fmt.Fprintln(os.Stdout, s)
	if w, _ := ctx.Value(ctxKey{}).(Writer); w != nil {
		w(Event{Type: "step", Detail: s})
	}
}

func Printf(ctx context.Context, format string, args ...any) {
	s := fmt.Sprintf(format, args...)
	s = trimNL(s)
	fmt.Fprintln(os.Stdout, s)
	if w, _ := ctx.Value(ctxKey{}).(Writer); w != nil {
		w(Event{Type: "step", Detail: s})
	}
}

func StartedAt(ctx context.Context) time.Time {
	return time.Now()
}

func trimNL(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
