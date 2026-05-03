package ghapiops

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestValidateConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  Config
		wantErr string
	}{
		{
			name: "auto is valid",
			config: Config{
				Backend:       BackendAuto,
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
		},
		{
			name: "invalid backend",
			config: Config{
				Backend:       "invalid",
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
			wantErr: "backend must be one of auto, random, rest, graphql",
		},
		{
			name: "negative rest weight",
			config: Config{
				Backend:       BackendRandom,
				RESTWeight:    -1,
				GraphQLWeight: 1,
			},
			wantErr: "rest-weight must be non-negative",
		},
		{
			name: "adaptive mode requires weight",
			config: Config{
				Backend:       BackendRandom,
				RESTWeight:    0,
				GraphQLWeight: 0,
			},
			wantErr: "adaptive polling requires rest-weight or graphql-weight to be positive",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateConfig(test.config)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateConfig() error = %v", err)
				}
				return
			}
			if err == nil || !containsAny(err.Error(), test.wantErr) {
				t.Fatalf("ValidateConfig() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestScaleWeight(t *testing.T) {
	t.Parallel()

	if got := scaleWeight(3, 10, 2); got != 15 {
		t.Fatalf("scaleWeight() = %d, want 15", got)
	}
	if got := scaleWeight(3, 10, 1); got != 30 {
		t.Fatalf("scaleWeight() with unit cost = %d, want 30", got)
	}
	if got := scaleWeight(3, 10, 3); got != 10 {
		t.Fatalf("scaleWeight() with non-unit cost = %d, want 10", got)
	}
}

func TestSelectBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    Config
		limits    *RateLimitSnapshot
		randomInt func(int64) int64
		want      []Backend
	}{
		{
			name: "rest fixed backend",
			config: Config{
				Backend:       BackendREST,
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
			randomInt: func(int64) int64 { return 0 },
			want:      []Backend{BackendREST},
		},
		{
			name: "auto prefers rest by default",
			config: Config{
				Backend:       BackendAuto,
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
			randomInt: func(int64) int64 { return 0 },
			want:      []Backend{BackendREST, BackendGraphQL},
		},
		{
			name: "auto honors configured weights",
			config: Config{
				Backend:       BackendAuto,
				RESTWeight:    1,
				GraphQLWeight: 2,
			},
			randomInt: func(int64) int64 { return 0 },
			want:      []Backend{BackendGraphQL, BackendREST},
		},
		{
			name: "auto can prefer graphql when adjusted weight is higher",
			config: Config{
				Backend:           BackendAuto,
				RESTWeight:        1,
				GraphQLWeight:     1,
				AutoAdjustWeights: true,
			},
			limits:    &RateLimitSnapshot{CoreRemaining: 1, GraphQLRemaining: 20},
			randomInt: func(int64) int64 { return 0 },
			want:      []Backend{BackendGraphQL, BackendREST},
		},
		{
			name: "auto adjust keeps rest preference when quotas are equal",
			config: Config{
				Backend:           BackendAuto,
				RESTWeight:        1,
				GraphQLWeight:     1,
				AutoAdjustWeights: true,
			},
			limits:    &RateLimitSnapshot{CoreRemaining: 10, GraphQLRemaining: 10},
			randomInt: func(int64) int64 { return 0 },
			want:      []Backend{BackendREST, BackendGraphQL},
		},
		{
			name: "random honors configured weights",
			config: Config{
				Backend:       BackendRandom,
				RESTWeight:    3,
				GraphQLWeight: 1,
			},
			randomInt: func(int64) int64 { return 3 },
			want:      []Backend{BackendGraphQL, BackendREST},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			order, err := SelectBackends(test.config, test.limits, test.randomInt)
			if err != nil {
				t.Fatalf("SelectBackends() error = %v", err)
			}
			if len(order) != len(test.want) {
				t.Fatalf("SelectBackends() len = %d, want %d", len(order), len(test.want))
			}
			for i := range order {
				if order[i] != test.want[i] {
					t.Fatalf("SelectBackends()[%d] = %q, want %q", i, order[i], test.want[i])
				}
			}
		})
	}
}

func TestChooseWeightedBackendHandlesLargeWeights(t *testing.T) {
	t.Parallel()

	backend, err := chooseWeightedBackend(maxInt64, maxInt64, func(total int64) int64 { return total - 1 })
	if err != nil {
		t.Fatalf("chooseWeightedBackend() error = %v", err)
	}
	if backend != BackendGraphQL {
		t.Fatalf("chooseWeightedBackend() = %q, want graphql", backend)
	}
}

func TestCachedRateLimitFetcher(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	nowCalls := 0
	source := &stubRateLimitFetcher{
		snapshots: []RateLimitSnapshot{
			{CoreRemaining: 10, GraphQLRemaining: 20},
			{CoreRemaining: 5, GraphQLRemaining: 15},
		},
	}
	cache := &CachedRateLimitFetcher{
		Fetcher:    source,
		MinRefresh: time.Minute,
		Now: func() time.Time {
			nowCalls++
			return now
		},
	}

	first, err := cache.Fetch(context.Background())
	if err != nil {
		t.Fatalf("CachedRateLimitFetcher.Fetch() error = %v", err)
	}
	second, err := cache.Fetch(context.Background())
	if err != nil {
		t.Fatalf("CachedRateLimitFetcher.Fetch() second error = %v", err)
	}
	if first != second {
		t.Fatalf("CachedRateLimitFetcher.Fetch() second = %#v, want %#v", second, first)
	}
	if source.calls != 1 {
		t.Fatalf("CachedRateLimitFetcher.Fetch() source calls = %d, want 1", source.calls)
	}
	if nowCalls != 2 {
		t.Fatalf("CachedRateLimitFetcher.Fetch() now calls after two fetches = %d, want 2", nowCalls)
	}

	now = now.Add(2 * time.Minute)
	third, err := cache.Fetch(context.Background())
	if err != nil {
		t.Fatalf("CachedRateLimitFetcher.Fetch() third error = %v", err)
	}
	if third.CoreRemaining != 5 || third.GraphQLRemaining != 15 {
		t.Fatalf("CachedRateLimitFetcher.Fetch() third = %#v, want refreshed snapshot", third)
	}
	if source.calls != 2 {
		t.Fatalf("CachedRateLimitFetcher.Fetch() source calls = %d, want 2", source.calls)
	}
	if nowCalls != 3 {
		t.Fatalf("CachedRateLimitFetcher.Fetch() total now calls = %d, want 3", nowCalls)
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
