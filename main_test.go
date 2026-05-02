package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/spf13/cobra"
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

func TestValidatePollingConfigForCommand(t *testing.T) {
	t.Parallel()

	cmd := &cobra.Command{Use: "test"}
	cmd.Flags().String("backend", string(pollingBackendAuto), "")
	cmd.Flags().Int("rest-weight", 1, "")
	cmd.Flags().Int("graphql-weight", 1, "")
	cmd.Flags().Bool("auto-adjust-weights", false, "")
	if err := cmd.Flags().Set("backend", string(pollingBackendREST)); err != nil {
		t.Fatalf("Set backend: %v", err)
	}
	if err := cmd.Flags().Set("rest-weight", "2"); err != nil {
		t.Fatalf("Set rest-weight: %v", err)
	}

	err := validatePollingConfigForCommand(cmd, pollingConfig{
		Backend:       pollingBackendREST,
		RESTWeight:    2,
		GraphQLWeight: 1,
	})
	if err == nil {
		t.Fatal("validatePollingConfigForCommand() error = nil, want adaptive-backend validation")
	}
	want := "--rest-weight requires --backend auto or random"
	if err.Error() != want {
		t.Fatalf("validatePollingConfigForCommand() error = %q, want %q", err.Error(), want)
	}
}

func TestScalePollingWeight(t *testing.T) {
	t.Parallel()

	if got := scalePollingWeight(3, 10, 2); got != 15 {
		t.Fatalf("scalePollingWeight() = %d, want 15", got)
	}
	if got := scalePollingWeight(3, 10, 1); got != 30 {
		t.Fatalf("scalePollingWeight() with unit cost = %d, want 30", got)
	}
}

func TestSelectPollingBackends(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		config    pollingConfig
		limits    *rateLimitSnapshot
		randomInt func(int64) int64
		want      []pollingBackend
	}{
		{
			name: "rest fixed backend",
			config: pollingConfig{
				Backend:       pollingBackendREST,
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
			randomInt: func(int64) int64 { return 0 },
			want:      []pollingBackend{pollingBackendREST},
		},
		{
			name: "auto prefers rest by default",
			config: pollingConfig{
				Backend:       pollingBackendAuto,
				RESTWeight:    1,
				GraphQLWeight: 1,
			},
			randomInt: func(int64) int64 { return 0 },
			want:      []pollingBackend{pollingBackendREST, pollingBackendGraphQL},
		},
		{
			name: "auto honors configured weights",
			config: pollingConfig{
				Backend:       pollingBackendAuto,
				RESTWeight:    1,
				GraphQLWeight: 2,
			},
			randomInt: func(int64) int64 { return 0 },
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
			randomInt: func(int64) int64 { return 0 },
			want:      []pollingBackend{pollingBackendGraphQL, pollingBackendREST},
		},
		{
			name: "auto adjust keeps rest preference when quotas are equal",
			config: pollingConfig{
				Backend:           pollingBackendAuto,
				RESTWeight:        1,
				GraphQLWeight:     1,
				AutoAdjustWeights: true,
			},
			limits:    &rateLimitSnapshot{CoreRemaining: 10, GraphQLRemaining: 10},
			randomInt: func(int64) int64 { return 0 },
			want:      []pollingBackend{pollingBackendREST, pollingBackendGraphQL},
		},
		{
			name: "random honors configured weights",
			config: pollingConfig{
				Backend:       pollingBackendRandom,
				RESTWeight:    3,
				GraphQLWeight: 1,
			},
			randomInt: func(int64) int64 { return 3 },
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

	firstPagePath := fmt.Sprintf("repos/apstndb/gh-copilot-review/pulls/3/reviews?per_page=%d", pullRequestReviewsPerPage)
	secondPagePath := fmt.Sprintf("repos/apstndb/gh-copilot-review/pulls/3/reviews?per_page=%d&page=2", pullRequestReviewsPerPage)
	firstPageReviews := []pullRequestReview{
		{
			User:        pullRequestReviewUser{Login: "copilot-pull-request-reviewer[bot]"},
			State:       "APPROVED",
			SubmittedAt: time.Unix(1, 0),
		},
	}
	pageTwo := []pullRequestReview{
		{
			User:        pullRequestReviewUser{Login: "reviewer"},
			State:       "COMMENTED",
			SubmittedAt: time.Unix(999, 0),
		},
	}

	client := &stubRESTGetter{
		responses: map[string]stubRESTResponse{
			firstPagePath: {
				body: firstPageReviews,
				headers: http.Header{
					"Link": []string{
						fmt.Sprintf("<https://api.github.com/%s>; rel=\"next\", <https://api.github.com/%s>; rel=\"last\"", secondPagePath, secondPagePath),
					},
				},
			},
			secondPagePath: {
				body: pageTwo,
			},
		},
	}

	reviews, err := fetchPullRequestReviewsREST(client, "apstndb", "gh-copilot-review", 3)
	if err != nil {
		t.Fatalf("fetchPullRequestReviewsREST() error = %v", err)
	}
	if len(reviews) != len(firstPageReviews) {
		t.Fatalf("fetchPullRequestReviewsREST() len = %d, want %d", len(reviews), len(firstPageReviews))
	}
	if reviews[0].State != "APPROVED" {
		t.Fatalf("fetchPullRequestReviewsREST() state = %q, want APPROVED", reviews[0].State)
	}
}

func TestFetchPullRequestReviewsRESTScansBackwardPages(t *testing.T) {
	t.Parallel()

	firstPagePath := fmt.Sprintf("repos/apstndb/gh-copilot-review/pulls/3/reviews?per_page=%d", pullRequestReviewsPerPage)
	thirdPagePath := fmt.Sprintf("repos/apstndb/gh-copilot-review/pulls/3/reviews?per_page=%d&page=3", pullRequestReviewsPerPage)
	secondPagePath, err := setPage(thirdPagePath, 2)
	if err != nil {
		t.Fatalf("setPage() error = %v", err)
	}

	client := &stubRESTGetter{
		responses: map[string]stubRESTResponse{
			firstPagePath: {
				body: []pullRequestReview{
					{
						User:        pullRequestReviewUser{Login: "reviewer"},
						State:       "COMMENTED",
						SubmittedAt: time.Unix(1, 0),
					},
				},
				headers: http.Header{
					"Link": []string{
						fmt.Sprintf("<https://api.github.com/%s>; rel=\"next\", <https://api.github.com/%s>; rel=\"last\"", secondPagePath, thirdPagePath),
					},
				},
			},
			secondPagePath: {
				body: []pullRequestReview{
					{
						User:        pullRequestReviewUser{Login: "copilot-pull-request-reviewer[bot]"},
						State:       "APPROVED",
						SubmittedAt: time.Unix(2, 0),
					},
				},
			},
			thirdPagePath: {
				body: []pullRequestReview{
					{
						User:        pullRequestReviewUser{Login: "reviewer"},
						State:       "COMMENTED",
						SubmittedAt: time.Unix(3, 0),
					},
				},
			},
		},
	}

	reviews, err := fetchPullRequestReviewsREST(client, "apstndb", "gh-copilot-review", 3)
	if err != nil {
		t.Fatalf("fetchPullRequestReviewsREST() error = %v", err)
	}
	if len(reviews) != 1 {
		t.Fatalf("fetchPullRequestReviewsREST() len = %d, want 1", len(reviews))
	}
	if reviews[0].User.Login != "copilot-pull-request-reviewer[bot]" {
		t.Fatalf("fetchPullRequestReviewsREST() reviewer = %q, want copilot page from backward scan", reviews[0].User.Login)
	}
	if client.requestCount(secondPagePath) != 1 || client.requestCount(thirdPagePath) != 1 {
		t.Fatalf("fetchPullRequestReviewsREST() backward scan requests = page2:%d page3:%d, want 1/1", client.requestCount(secondPagePath), client.requestCount(thirdPagePath))
	}
}

func TestFetchReviewStatusRESTSkipsReviewsWhilePending(t *testing.T) {
	t.Parallel()

	client := &stubRESTGetter{
		responses: map[string]stubRESTResponse{
			"repos/apstndb/gh-copilot-review/pulls/3/requested_reviewers": {
				body: requestedReviewersResponse{
					Users: []requestedReviewerUser{{Login: "copilot[bot]"}},
				},
			},
		},
	}

	status, err := fetchReviewStatusRESTWithRequester(client, "apstndb", "gh-copilot-review", 3)
	if err != nil {
		t.Fatalf("fetchReviewStatusREST() error = %v", err)
	}
	if !status.CopilotRequested {
		t.Fatal("fetchReviewStatusREST() did not keep pending Copilot request")
	}
	if client.requestCount("repos/apstndb/gh-copilot-review/pulls/3/requested_reviewers") != 1 {
		t.Fatal("fetchReviewStatusREST() did not query requested_reviewers exactly once")
	}
	if client.totalRequests() != 1 {
		t.Fatalf("fetchReviewStatusREST() total requests = %d, want 1", client.totalRequests())
	}
}

func TestChooseWeightedBackendHandlesLargeWeights(t *testing.T) {
	t.Parallel()

	backend, err := chooseWeightedBackend(maxInt64, maxInt64, func(total int64) int64 { return total - 1 })
	if err != nil {
		t.Fatalf("chooseWeightedBackend() error = %v", err)
	}
	if backend != pollingBackendGraphQL {
		t.Fatalf("chooseWeightedBackend() = %q, want graphql", backend)
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

type stubRESTResponse struct {
	body    interface{}
	headers http.Header
	status  int
}

type stubRESTGetter struct {
	responses map[string]stubRESTResponse
	requests  []string
}

func (g *stubRESTGetter) Request(method, path string, body io.Reader) (*http.Response, error) {
	g.requests = append(g.requests, path)
	response, ok := g.responses[path]
	if !ok {
		response = stubRESTResponse{body: []pullRequestReview{}}
	}
	payload, err := json.Marshal(response.body)
	if err != nil {
		return nil, err
	}
	status := response.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Header:     response.headers.Clone(),
		Body:       io.NopCloser(bytes.NewReader(payload)),
	}, nil
}

func (g *stubRESTGetter) totalRequests() int {
	return len(g.requests)
}

func (g *stubRESTGetter) requestCount(path string) int {
	count := 0
	for _, requestPath := range g.requests {
		if requestPath == path {
			count++
		}
	}
	return count
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
