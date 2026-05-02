package ghapiops

import (
	"context"
	"time"
)

type RateLimitSnapshot struct {
	CoreRemaining    int
	GraphQLRemaining int
}

type RateLimitFetcher interface {
	Fetch(context.Context) (RateLimitSnapshot, error)
}

type CachedRateLimitFetcher struct {
	Fetcher    RateLimitFetcher
	MinRefresh time.Duration
	Now        func() time.Time

	cached    RateLimitSnapshot
	cachedAt  time.Time
	hasCached bool
}

func (f *CachedRateLimitFetcher) Fetch(ctx context.Context) (RateLimitSnapshot, error) {
	nowFunc := f.Now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc()
	if f.hasCached && now.Sub(f.cachedAt) < f.MinRefresh {
		return f.cached, nil
	}

	snapshot, err := f.Fetcher.Fetch(ctx)
	if err != nil {
		return RateLimitSnapshot{}, err
	}
	f.cached = snapshot
	f.cachedAt = now
	f.hasCached = true
	return snapshot, nil
}
