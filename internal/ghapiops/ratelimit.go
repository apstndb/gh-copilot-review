package ghapiops

import (
	"context"
	"errors"
	"sync"
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

	mu        sync.Mutex
	cached    RateLimitSnapshot
	cachedAt  time.Time
	hasCached bool
}

func (f *CachedRateLimitFetcher) Fetch(ctx context.Context) (RateLimitSnapshot, error) {
	if f.Fetcher == nil {
		return RateLimitSnapshot{}, errors.New("cached rate limit fetcher requires a fetcher")
	}

	nowFunc := f.Now
	if nowFunc == nil {
		nowFunc = time.Now
	}
	now := nowFunc()

	f.mu.Lock()
	if f.hasCached && now.Sub(f.cachedAt) < f.MinRefresh {
		cached := f.cached
		f.mu.Unlock()
		return cached, nil
	}
	f.mu.Unlock()

	snapshot, err := f.Fetcher.Fetch(ctx)
	if err != nil {
		return RateLimitSnapshot{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.hasCached && now.Sub(f.cachedAt) < f.MinRefresh {
		return f.cached, nil
	}
	f.cached = snapshot
	f.cachedAt = now
	f.hasCached = true
	return snapshot, nil
}
