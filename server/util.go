package server

import (
	"context"
	"time"
)

func seconds(n int) time.Duration { return time.Duration(n) * time.Second }

func contextWithTimeout(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, d)
}
