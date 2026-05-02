package main

import (
	"errors"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

func TestValidatePollingConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  pollingConfig
		wantErr string
	}{
		{
			name: "auto is valid",
			config: pollingConfig{
				Backend:       pollingBackendAuto,
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
		},
		{
			name: "invalid backend",
			config: pollingConfig{
				Backend:       "invalid",
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
			wantErr: "backend must be one of auto, random, rest, graphql",
		},
		{
			name: "negative rest weight",
			config: pollingConfig{
				Backend:       pollingBackendRandom,
				RESTWeight:    -1,
				GraphQLWeight: 1,
			},
			wantErr: "rest-weight must be non-negative",
		},
		{
			name: "adaptive mode requires weight",
			config: pollingConfig{
				Backend:       pollingBackendRandom,
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

			err := validatePollingConfig(test.config)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("validatePollingConfig() error = %v", err)
				}
				return
			}
			if err == nil || !containsAny(err.Error(), test.wantErr) {
				t.Fatalf("validatePollingConfig() error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestSelectPollingBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    pollingConfig
		limits    *rateLimitSnapshot
		randomInt func(int) int
		want      []pollingBackend
	}{
		{
			name: "rest fixed backend",
			config: pollingConfig{
				Backend:       pollingBackendREST,
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
			randomInt: func(int) int { return 0 },
			want:      []pollingBackend{pollingBackendREST},
		},
		{
			name: "auto prefers rest by default",
			config: pollingConfig{
				Backend:       pollingBackendAuto,
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
			randomInt: func(int) int { return 0 },
			want:      []pollingBackend{pollingBackendREST, pollingBackendGraphQL},
		},
		{
			name: "auto honors configured weights",
			config: pollingConfig{
				Backend:       pollingBackendAuto,
				RESTWeight:    1,
				GraphQLWeight: 2,
			},
			randomInt: func(int) int { return 0 },
			want:      []pollingBackend{pollingBackendGraphQL, pollingBackendREST},
		},
		{
			name: "auto can prefer graphql when adjusted weight is higher",
			config: pollingConfig{
				Backend:           pollingBackendAuto,
				RESTWeight:        1,
				GraphQLWeight:     1,
				AutoAdjustWeights: true,
			},
			limits:    &rateLimitSnapshot{CoreRemaining: 1, GraphQLRemaining: 20},
			randomInt: func(int) int { return 0 },
			want:      []pollingBackend{pollingBackendGraphQL, pollingBackendREST},
		},
		{
			name: "random honors configured weights",
			config: pollingConfig{
				Backend:       pollingBackendRandom,
				RESTWeight:    3,
				GraphQLWeight: 1,
			},
			randomInt: func(int) int { return 3 },
			want:      []pollingBackend{pollingBackendGraphQL, pollingBackendREST},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			order, err := selectPollingBackends(test.config, test.limits, test.randomInt)
			if err != nil {
				t.Fatalf("selectPollingBackends() error = %v", err)
			}
			if len(order) != len(test.want) {
				t.Fatalf("selectPollingBackends() len = %d, want %d", len(order), len(test.want))
			}
			for i := range order {
				if order[i] != test.want[i] {
					t.Fatalf("selectPollingBackends()[%d] = %q, want %q", i, order[i], test.want[i])
				}
			}
		})
	}
}

func TestBuildReviewStatusFromREST(t *testing.T) {
	t.Parallel()

	status := buildReviewStatusFromREST(
		requestedReviewersResponse{
			Users: []requestedReviewerUser{
				{Login: "Copilot[bot]"},
			},
		},
		[]pullRequestReview{
			{
				User:        pullRequestReviewUser{Login: "someone"},
				State:       "COMMENTED",
				SubmittedAt: time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC),
				CommitID:    "ignore",
			},
			{
				User:        pullRequestReviewUser{Login: "copilot-pull-request-reviewer[bot]"},
				State:       "COMMENTED",
				SubmittedAt: time.Date(2026, 5, 2, 1, 0, 0, 0, time.UTC),
				CommitID:    "older",
			},
			{
				User:        pullRequestReviewUser{Login: "copilot-pull-request-reviewer[bot]"},
				State:       "APPROVED",
				SubmittedAt: time.Date(2026, 5, 2, 2, 0, 0, 0, time.UTC),
				CommitID:    "newer",
			},
		},
	)

	if !status.CopilotRequested {
		t.Fatal("buildReviewStatusFromREST() did not mark Copilot request")
	}
	if status.LatestCopilotReview == nil {
		t.Fatal("buildReviewStatusFromREST() did not keep latest Copilot review")
	}
	if status.LatestCopilotReview.State != "APPROVED" {
		t.Fatalf("buildReviewStatusFromREST() state = %q, want APPROVED", status.LatestCopilotReview.State)
	}
}

func TestFetchPullRequestReviewsREST(t *testing.T) {
	t.Parallel()

	pageOne := make([]pullRequestReview, pullRequestReviewsPerPage)
	for index := range pageOne {
		pageOne[index] = pullRequestReview{
			User:        pullRequestReviewUser{Login: "reviewer"},
			State:       "COMMENTED",
			SubmittedAt: time.Unix(int64(index), 0),
		}
	}
	pageTwo := []pullRequestReview{
		{
			User:        pullRequestReviewUser{Login: "copilot-pull-request-reviewer[bot]"},
			State:       "APPROVED",
			SubmittedAt: time.Unix(999, 0),
		},
	}

	client := &stubRESTGetter{
		reviewPages: map[string][]pullRequestReview{
			"repos/apstndb/gh-copilot-review/pulls/3/reviews?per_page=100&page=1": pageOne,
			"repos/apstndb/gh-copilot-review/pulls/3/reviews?per_page=100&page=2": pageTwo,
		},
	}

	reviews, err := fetchPullRequestReviewsREST(client, "apstndb", "gh-copilot-review", 3)
	if err != nil {
		t.Fatalf("fetchPullRequestReviewsREST() error = %v", err)
	}
	if len(reviews) != len(pageOne)+len(pageTwo) {
		t.Fatalf("fetchPullRequestReviewsREST() len = %d, want %d", len(reviews), len(pageOne)+len(pageTwo))
	}
}

func TestFetchReviewStatusWithFallback(t *testing.T) {
	t.Parallel()

	t.Run("falls back on rate limit", func(t *testing.T) {
		t.Parallel()

		rest := &stubReviewStatusFetcher{
			err: &api.HTTPError{StatusCode: 403, Message: "API rate limit exceeded"},
		}
		graphql := &stubReviewStatusFetcher{
			status: reviewStatus{CopilotRequested: true},
		}

		status, err := fetchReviewStatusWithFallback(
			[]pollingBackend{pollingBackendREST, pollingBackendGraphQL},
			map[pollingBackend]reviewStatusFetcher{
				pollingBackendREST:    rest,
				pollingBackendGraphQL: graphql,
			},
			"apstndb",
			"gh-copilot-review",
			2,
		)
		if err != nil {
			t.Fatalf("fetchReviewStatusWithFallback() error = %v", err)
		}
		if !status.CopilotRequested {
			t.Fatal("fetchReviewStatusWithFallback() did not return fallback status")
		}
		if rest.calls != 1 || graphql.calls != 1 {
			t.Fatalf("fetchReviewStatusWithFallback() calls = rest:%d graphql:%d, want 1/1", rest.calls, graphql.calls)
		}
	})

	t.Run("does not fall back on non-retryable errors", func(t *testing.T) {
		t.Parallel()

		rest := &stubReviewStatusFetcher{
			err: &api.HTTPError{StatusCode: 404, Message: "Not Found"},
		}
		graphql := &stubReviewStatusFetcher{
			status: reviewStatus{CopilotRequested: true},
		}

		_, err := fetchReviewStatusWithFallback(
			[]pollingBackend{pollingBackendREST, pollingBackendGraphQL},
			map[pollingBackend]reviewStatusFetcher{
				pollingBackendREST:    rest,
				pollingBackendGraphQL: graphql,
			},
			"apstndb",
			"gh-copilot-review",
			2,
		)
		if err == nil {
			t.Fatal("fetchReviewStatusWithFallback() error = nil, want non-retryable error")
		}
		if !containsAny(err.Error(), "rest backend") {
			t.Fatalf("fetchReviewStatusWithFallback() error = %v, want backend context", err)
		}
		if graphql.calls != 0 {
			t.Fatalf("fetchReviewStatusWithFallback() graphql calls = %d, want 0", graphql.calls)
		}
	})
}

func TestIsFallbackEligibleError(t *testing.T) {
	t.Parallel()

	if !isFallbackEligibleError(&api.GraphQLError{Errors: []api.GraphQLErrorItem{{Message: "API rate limit exceeded"}}}) {
		t.Fatal("isFallbackEligibleError() = false for GraphQL rate limit")
	}
	if isFallbackEligibleError(errors.New("plain error")) {
		t.Fatal("isFallbackEligibleError() = true for plain error")
	}
}

func TestCachedRateLimitFetcher(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	source := &stubRateLimitFetcher{
		snapshots: []rateLimitSnapshot{
			{CoreRemaining: 10, GraphQLRemaining: 20},
			{CoreRemaining: 5, GraphQLRemaining: 15},
		},
	}
	cache := &cachedRateLimitFetcher{
		fetcher:    source,
		minRefresh: time.Minute,
		now: func() time.Time {
			return now
		},
	}

	first, err := cache.Fetch()
	if err != nil {
		t.Fatalf("cachedRateLimitFetcher.Fetch() error = %v", err)
	}
	second, err := cache.Fetch()
	if err != nil {
		t.Fatalf("cachedRateLimitFetcher.Fetch() second error = %v", err)
	}
	if first != second {
		t.Fatalf("cachedRateLimitFetcher.Fetch() second = %#v, want %#v", second, first)
	}
	if source.calls != 1 {
		t.Fatalf("cachedRateLimitFetcher.Fetch() source calls = %d, want 1", source.calls)
	}

	now = now.Add(2 * time.Minute)
	third, err := cache.Fetch()
	if err != nil {
		t.Fatalf("cachedRateLimitFetcher.Fetch() third error = %v", err)
	}
	if third.CoreRemaining != 5 || third.GraphQLRemaining != 15 {
		t.Fatalf("cachedRateLimitFetcher.Fetch() third = %#v, want refreshed snapshot", third)
	}
	if source.calls != 2 {
		t.Fatalf("cachedRateLimitFetcher.Fetch() source calls = %d, want 2", source.calls)
	}
}

type stubReviewStatusFetcher struct {
	status reviewStatus
	err    error
	calls  int
}

func (f *stubReviewStatusFetcher) Fetch(string, string, int) (reviewStatus, error) {
	f.calls++
	if f.err != nil {
		return reviewStatus{}, f.err
	}
	return f.status, nil
}

type stubRESTGetter struct {
	reviewPages map[string][]pullRequestReview
}

func (g *stubRESTGetter) Get(path string, resp interface{}) error {
	reviews, ok := g.reviewPages[path]
	if !ok {
		reviews = []pullRequestReview{}
	}
	target, ok := resp.(*[]pullRequestReview)
	if !ok {
		return errors.New("unexpected response type")
	}
	*target = append((*target)[:0], reviews...)
	return nil
}

type stubRateLimitFetcher struct {
	snapshots []rateLimitSnapshot
	calls     int
}

func (f *stubRateLimitFetcher) Fetch() (rateLimitSnapshot, error) {
	if f.calls >= len(f.snapshots) {
		return rateLimitSnapshot{}, errors.New("no snapshot")
	}
	snapshot := f.snapshots[f.calls]
	f.calls++
	return snapshot, nil
}
