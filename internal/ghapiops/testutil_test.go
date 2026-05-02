package ghapiops

import (
	"context"
	"errors"
)

type stubRateLimitFetcher struct {
	snapshots []RateLimitSnapshot
	calls     int
}

func (f *stubRateLimitFetcher) Fetch(context.Context) (RateLimitSnapshot, error) {
	if f.calls >= len(f.snapshots) {
		return RateLimitSnapshot{}, errors.New("no snapshot")
	}
	snapshot := f.snapshots[f.calls]
	f.calls++
	return snapshot, nil
}
