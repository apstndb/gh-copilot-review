package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	gh "github.com/cli/go-gh/v2"
	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/spf13/cobra"
)

const (
	copilotRequestLogin       = "copilot"
	copilotReviewLogin        = "copilot-pull-request-reviewer"
	pullRequestReviewsPerPage = 100
	restPollingRequestCost    = 2
	graphQLPollingRequestCost = 1
)

type pollingBackend string

const (
	pollingBackendAuto    pollingBackend = "auto"
	pollingBackendRandom  pollingBackend = "random"
	pollingBackendGraphQL pollingBackend = "graphql"
	pollingBackendREST    pollingBackend = "rest"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		var pendingErr pendingReviewError
		if errors.As(err, &pendingErr) {
			fmt.Fprintln(os.Stderr, pendingErr.Error())
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "gh-copilot-review",
		Short:         "Manage GitHub Copilot pull request reviews",
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(newRequestCmd())
	cmd.AddCommand(newCheckCmd())
	return cmd
}

func newRequestCmd() *cobra.Command {
	var wait bool
	var interval int
	var timeout int
	var backend string
	var restWeight int
	var graphqlWeight int
	var autoAdjustWeights bool

	cmd := &cobra.Command{
		Use:   "request [<pr>]",
		Short: "Request or re-request review from Copilot on a pull request",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selector := ""
			if len(args) == 1 {
				selector = args[0]
			}
			if shouldValidatePollingFlags(cmd, wait) {
				if err := validatePollingFlags(interval, timeout); err != nil {
					return err
				}
			}

			polling := newPollingConfig(backend, restWeight, graphqlWeight, autoAdjustWeights)
			if shouldValidateBackendFlags(cmd, wait) {
				if err := validatePollingConfigForCommand(cmd, polling); err != nil {
					return err
				}
			}

			ghArgs := []string{"pr", "edit"}
			if selector != "" {
				ghArgs = append(ghArgs, selector)
			}
			ghArgs = append(ghArgs, "--add-reviewer", "@copilot")

			stdout, stderr, err := gh.Exec(ghArgs...)
			if stdout.Len() > 0 {
				fmt.Print(stdout.String())
			}
			if stderr.Len() > 0 {
				fmt.Fprint(os.Stderr, stderr.String())
			}
			if err != nil {
				return err
			}
			if wait {
				return pollReviewStatus(selector, interval, timeout, false, polling)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&wait, "wait", false, "wait for Copilot review after requesting it")
	cmd.Flags().IntVar(&interval, "interval", 15, "poll interval in seconds with --wait; validated but otherwise ignored without --wait")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "stop waiting after N seconds with --wait (0 disables timeout); validated but otherwise ignored without --wait")
	cmd.Flags().StringVar(&backend, "backend", string(pollingBackendAuto), "status polling backend with --wait: auto, random, rest, or graphql")
	cmd.Flags().IntVar(&restWeight, "rest-weight", 1, "relative REST weight for adaptive polling backends; validated but otherwise ignored without --wait")
	cmd.Flags().IntVar(&graphqlWeight, "graphql-weight", 1, "relative GraphQL weight for adaptive polling backends; validated but otherwise ignored without --wait")
	cmd.Flags().BoolVar(&autoAdjustWeights, "auto-adjust-weights", false, "adapt backend weights from current rate limits for adaptive polling backends; ignored without --wait")
	return cmd
}

func newCheckCmd() *cobra.Command {
	var interval int
	var timeout int
	var async bool
	var backend string
	var restWeight int
	var graphqlWeight int
	var autoAdjustWeights bool

	cmd := &cobra.Command{
		Use:   "check [<pr>]",
		Short: "Check Copilot review status on a pull request",
		Long: "Poll until the Copilot review is no longer pending (default). " +
			"With --async, perform a single poll and return a non-zero exit status while a review is still requested.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if shouldValidatePollingFlags(cmd, !async) {
				if err := validatePollingFlags(interval, timeout); err != nil {
					return err
				}
			}

			polling := newPollingConfig(backend, restWeight, graphqlWeight, autoAdjustWeights)
			if err := validatePollingConfigForCommand(cmd, polling); err != nil {
				return err
			}

			selector := ""
			if len(args) == 1 {
				selector = args[0]
			}
			return pollReviewStatus(selector, interval, timeout, async, polling)
		},
	}

	cmd.Flags().IntVar(&interval, "interval", 15, "poll interval in seconds when waiting; provided values are still validated with --async")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "stop waiting after N seconds (0 disables timeout); provided values are still validated with --async")
	cmd.Flags().BoolVar(&async, "async", false, "perform a single check and exit immediately; returns non-zero while review is still pending")
	cmd.Flags().StringVar(&backend, "backend", string(pollingBackendAuto), "status polling backend: auto, random, rest, or graphql")
	cmd.Flags().IntVar(&restWeight, "rest-weight", 1, "relative REST weight for adaptive polling backends")
	cmd.Flags().IntVar(&graphqlWeight, "graphql-weight", 1, "relative GraphQL weight for adaptive polling backends")
	cmd.Flags().BoolVar(&autoAdjustWeights, "auto-adjust-weights", false, "adapt backend weights from current rate limits for adaptive polling backends")
	return cmd
}

type pollingConfig struct {
	Backend           pollingBackend
	RESTWeight        int
	GraphQLWeight     int
	AutoAdjustWeights bool
}

func newPollingConfig(backend string, restWeight, graphqlWeight int, autoAdjustWeights bool) pollingConfig {
	return pollingConfig{
		Backend:           pollingBackend(strings.ToLower(backend)),
		RESTWeight:        restWeight,
		GraphQLWeight:     graphqlWeight,
		AutoAdjustWeights: autoAdjustWeights,
	}
}

func pollReviewStatus(selector string, interval, timeout int, async bool, polling pollingConfig) error {
	target, err := resolvePR(selector)
	if err != nil {
		return err
	}
	repo, err := repository.Current()
	if err != nil {
		return fmt.Errorf("resolve current repository: %w", err)
	}

	fetchers := make(map[pollingBackend]reviewStatusFetcher, 2)
	var rateLimits rateLimitFetcher
	if polling.Backend != pollingBackendREST {
		client, err := api.DefaultGraphQLClient()
		if err != nil {
			return fmt.Errorf("build GraphQL client: %w", err)
		}
		fetchers[pollingBackendGraphQL] = graphQLReviewStatusFetcher{client: client}
	}
	if polling.Backend != pollingBackendGraphQL {
		client, err := api.DefaultRESTClient()
		if err != nil {
			return fmt.Errorf("build REST client: %w", err)
		}
		fetchers[pollingBackendREST] = restReviewStatusFetcher{client: client}
		if polling.AutoAdjustWeights && (polling.Backend == pollingBackendAuto || polling.Backend == pollingBackendRandom) {
			rateLimits = &cachedRateLimitFetcher{
				fetcher:    restRateLimitFetcher{client: client},
				minRefresh: time.Minute,
				now:        time.Now,
			}
		}
	}

	random := rand.New(rand.NewSource(time.Now().UnixNano()))
	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(time.Duration(timeout) * time.Second)
	}

	for {
		var snapshot *rateLimitSnapshot
		if rateLimits != nil {
			limits, err := rateLimits.Fetch()
			if err == nil {
				snapshot = &limits
			}
		}

		order, err := selectPollingBackends(polling, snapshot, random.Intn)
		if err != nil {
			return err
		}
		status, err := fetchReviewStatusWithFallback(order, fetchers, repo.Owner, repo.Name, target.Number)
		if err != nil {
			return err
		}

		if status.CopilotRequested {
			if async {
				return pendingReviewError{URL: target.URL}
			}
			fmt.Fprintf(os.Stderr, "%s awaiting review from Copilot on %s\n", time.Now().Format("2006-01-02 15:04:05"), target.URL)
			if !deadline.IsZero() && time.Now().Add(time.Duration(interval)*time.Second).After(deadline) {
				return fmt.Errorf("timed out waiting for Copilot review on %s", target.URL)
			}
			time.Sleep(time.Duration(interval) * time.Second)
			continue
		}

		if status.LatestCopilotReview != nil {
			fmt.Printf("Copilot review is no longer pending on %s: %s at %s\n", target.URL, status.LatestCopilotReview.State, status.LatestCopilotReview.SubmittedAt.Format(time.RFC3339))
		} else {
			fmt.Printf("Copilot review is no longer pending on %s.\n", target.URL)
		}
		return nil
	}
}

func shouldValidatePollingFlags(cmd *cobra.Command, active bool) bool {
	return active || cmd.Flags().Changed("interval") || cmd.Flags().Changed("timeout")
}

func shouldValidateBackendFlags(cmd *cobra.Command, active bool) bool {
	return active ||
		cmd.Flags().Changed("backend") ||
		cmd.Flags().Changed("rest-weight") ||
		cmd.Flags().Changed("graphql-weight") ||
		cmd.Flags().Changed("auto-adjust-weights")
}

func validatePollingFlags(interval, timeout int) error {
	if interval < 1 {
		return fmt.Errorf("interval must be positive: %d", interval)
	}
	if timeout < 0 {
		return fmt.Errorf("timeout must be non-negative: %d", timeout)
	}
	return nil
}

func validatePollingConfig(config pollingConfig) error {
	switch config.Backend {
	case pollingBackendAuto, pollingBackendRandom, pollingBackendGraphQL, pollingBackendREST:
	default:
		return fmt.Errorf("backend must be one of auto, random, rest, graphql: %q", config.Backend)
	}
	if config.RESTWeight < 0 {
		return fmt.Errorf("rest-weight must be non-negative: %d", config.RESTWeight)
	}
	if config.GraphQLWeight < 0 {
		return fmt.Errorf("graphql-weight must be non-negative: %d", config.GraphQLWeight)
	}
	if (config.Backend == pollingBackendAuto || config.Backend == pollingBackendRandom) &&
		config.RESTWeight == 0 && config.GraphQLWeight == 0 {
		return errors.New("adaptive polling requires rest-weight or graphql-weight to be positive")
	}
	return nil
}

func validatePollingConfigForCommand(cmd *cobra.Command, config pollingConfig) error {
	if err := validatePollingConfig(config); err != nil {
		return err
	}
	if config.Backend == pollingBackendAuto || config.Backend == pollingBackendRandom {
		return nil
	}
	if cmd.Flags().Changed("rest-weight") || cmd.Flags().Changed("graphql-weight") || cmd.Flags().Changed("auto-adjust-weights") {
		return errors.New("rest-weight, graphql-weight, and auto-adjust-weights require --backend auto or random")
	}
	return nil
}

func selectPollingBackends(config pollingConfig, limits *rateLimitSnapshot, randomIntn func(int) int) ([]pollingBackend, error) {
	switch config.Backend {
	case pollingBackendGraphQL:
		return []pollingBackend{pollingBackendGraphQL}, nil
	case pollingBackendREST:
		return []pollingBackend{pollingBackendREST}, nil
	case pollingBackendAuto:
		primary := preferredAutoBackend(config, limits)
		return pollingBackendOrder(primary), nil
	case pollingBackendRandom:
		restWeight, graphqlWeight := effectivePollingWeights(config, limits)
		primary, err := chooseWeightedBackend(restWeight, graphqlWeight, randomIntn)
		if err != nil {
			return nil, err
		}
		return pollingBackendOrder(primary), nil
	default:
		return nil, fmt.Errorf("unsupported polling backend: %q", config.Backend)
	}
}

func preferredAutoBackend(config pollingConfig, limits *rateLimitSnapshot) pollingBackend {
	restWeight, graphqlWeight := effectivePollingWeights(config, limits)
	if graphqlWeight > restWeight {
		return pollingBackendGraphQL
	}
	return pollingBackendREST
}

func effectivePollingWeights(config pollingConfig, limits *rateLimitSnapshot) (restWeight, graphqlWeight int) {
	restWeight = config.RESTWeight
	graphqlWeight = config.GraphQLWeight
	if !config.AutoAdjustWeights || limits == nil {
		return restWeight, graphqlWeight
	}

	adjustedREST := restWeight
	adjustedGraphQL := graphqlWeight
	if adjustedREST > 0 {
		adjustedREST *= max(limits.CoreRemaining, 0) * graphQLPollingRequestCost
	}
	if adjustedGraphQL > 0 {
		// REST polling currently needs requested_reviewers + reviews, while GraphQL uses one query.
		adjustedGraphQL *= max(limits.GraphQLRemaining, 0) * restPollingRequestCost
	}
	if adjustedREST == 0 && adjustedGraphQL == 0 {
		return restWeight, graphqlWeight
	}
	return adjustedREST, adjustedGraphQL
}

func chooseWeightedBackend(restWeight, graphqlWeight int, randomIntn func(int) int) (pollingBackend, error) {
	total := restWeight + graphqlWeight
	if total <= 0 {
		return "", errors.New("adaptive polling requires rest-weight or graphql-weight to be positive")
	}
	if randomIntn(total) < restWeight {
		return pollingBackendREST, nil
	}
	return pollingBackendGraphQL, nil
}

func pollingBackendOrder(primary pollingBackend) []pollingBackend {
	if primary == pollingBackendGraphQL {
		return []pollingBackend{pollingBackendGraphQL, pollingBackendREST}
	}
	return []pollingBackend{pollingBackendREST, pollingBackendGraphQL}
}

type reviewStatusFetcher interface {
	Fetch(owner, repo string, number int) (reviewStatus, error)
}

type graphQLReviewStatusFetcher struct {
	client *api.GraphQLClient
}

func (f graphQLReviewStatusFetcher) Fetch(owner, repo string, number int) (reviewStatus, error) {
	return fetchReviewStatusGraphQL(f.client, owner, repo, number)
}

type restReviewStatusFetcher struct {
	client *api.RESTClient
}

func (f restReviewStatusFetcher) Fetch(owner, repo string, number int) (reviewStatus, error) {
	return fetchReviewStatusREST(f.client, owner, repo, number)
}

type rateLimitFetcher interface {
	Fetch() (rateLimitSnapshot, error)
}

type restRequester interface {
	Request(method string, path string, body io.Reader) (*http.Response, error)
}

type restRateLimitFetcher struct {
	client *api.RESTClient
}

func (f restRateLimitFetcher) Fetch() (rateLimitSnapshot, error) {
	return fetchRateLimitSnapshot(f.client)
}

type cachedRateLimitFetcher struct {
	fetcher    rateLimitFetcher
	minRefresh time.Duration
	now        func() time.Time

	cached    rateLimitSnapshot
	cachedAt  time.Time
	hasCached bool
}

func (f *cachedRateLimitFetcher) Fetch() (rateLimitSnapshot, error) {
	now := time.Now
	if f.now != nil {
		now = f.now
	}
	if f.hasCached && now().Sub(f.cachedAt) < f.minRefresh {
		return f.cached, nil
	}

	snapshot, err := f.fetcher.Fetch()
	if err != nil {
		return rateLimitSnapshot{}, err
	}
	f.cached = snapshot
	f.cachedAt = now()
	f.hasCached = true
	return snapshot, nil
}

func getRESTJSON(client restRequester, path string, response interface{}) (http.Header, error) {
	resp, err := client.Request("GET", path, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, api.HandleHTTPError(resp)
	}
	if err := json.NewDecoder(resp.Body).Decode(response); err != nil {
		return nil, fmt.Errorf("decode REST response for %s: %w", path, err)
	}
	return resp.Header.Clone(), nil
}

func fetchReviewStatusWithFallback(order []pollingBackend, fetchers map[pollingBackend]reviewStatusFetcher, owner, repo string, number int) (reviewStatus, error) {
	var errs []error
	for index, backend := range order {
		fetcher, ok := fetchers[backend]
		if !ok {
			return reviewStatus{}, fmt.Errorf("polling backend unavailable: %s", backend)
		}

		status, err := fetcher.Fetch(owner, repo, number)
		if err == nil {
			return status, nil
		}

		wrappedErr := fmt.Errorf("%s backend: %w", backend, err)
		if index == len(order)-1 || len(order) == 1 || !isFallbackEligibleError(err) {
			if len(errs) == 0 {
				return reviewStatus{}, wrappedErr
			}
			return reviewStatus{}, errors.Join(append(errs, wrappedErr)...)
		}
		errs = append(errs, wrappedErr)
	}

	if len(errs) > 0 {
		return reviewStatus{}, errors.Join(errs...)
	}
	return reviewStatus{}, errors.New("no polling backend selected")
}

func isFallbackEligibleError(err error) bool {
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) {
		if httpErr.StatusCode == 429 || httpErr.StatusCode >= 500 {
			return true
		}
		if httpErr.StatusCode == 403 {
			return containsAny(strings.ToLower(httpErr.Message), "rate limit", "secondary rate", "abuse")
		}
	}

	var graphQLErr *api.GraphQLError
	if errors.As(err, &graphQLErr) {
		return containsAny(strings.ToLower(graphQLErr.Error()), "rate limit", "secondary rate", "abuse", "timeout", "temporarily", "unavailable")
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var temporaryErr interface{ Temporary() bool }
	if errors.As(err, &temporaryErr) && temporaryErr.Temporary() {
		return true
	}

	return false
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

type pendingReviewError struct {
	URL string
}

func (e pendingReviewError) Error() string {
	return fmt.Sprintf("Copilot review is still pending on %s", e.URL)
}

type prTarget struct {
	Number int    `json:"number"`
	URL    string `json:"url"`
}

func resolvePR(selector string) (prTarget, error) {
	args := []string{"pr", "view"}
	if selector != "" {
		args = append(args, selector)
	}
	args = append(args, "--json", "number,url")

	stdout, stderr, err := gh.Exec(args...)
	if stderr.Len() > 0 {
		fmt.Fprint(os.Stderr, stderr.String())
	}
	if err != nil {
		return prTarget{}, err
	}

	var target prTarget
	if err := json.Unmarshal(stdout.Bytes(), &target); err != nil {
		return prTarget{}, fmt.Errorf("decode gh pr view output: %w", err)
	}
	return target, nil
}

type reviewStatus struct {
	CopilotRequested    bool
	LatestCopilotReview *latestReview
}

type latestReview struct {
	State       string
	SubmittedAt time.Time
}

type reviewStatusResponse struct {
	Repository struct {
		PullRequest struct {
			ReviewRequests struct {
				Nodes []struct {
					RequestedReviewer struct {
						Typename string `json:"__typename"`
						Login    string `json:"login"`
					} `json:"requestedReviewer"`
				} `json:"nodes"`
			} `json:"reviewRequests"`
			LatestReviews struct {
				Nodes []struct {
					Author struct {
						Login string `json:"login"`
					} `json:"author"`
					State       string    `json:"state"`
					SubmittedAt time.Time `json:"submittedAt"`
				} `json:"nodes"`
			} `json:"latestReviews"`
		} `json:"pullRequest"`
	} `json:"repository"`
}

func fetchReviewStatusGraphQL(client *api.GraphQLClient, owner, repo string, number int) (reviewStatus, error) {
	const query = `
	query($owner:String!, $repo:String!, $number:Int!) {
	  repository(owner:$owner, name:$repo) {
	    pullRequest(number:$number) {
	      reviewRequests(first:20) {
	        nodes {
	          requestedReviewer {
	            __typename
	            ... on User { login }
	            ... on Bot { login }
	          }
	        }
	      }
	      latestReviews(first:20) {
	        nodes {
	          author { login }
	          state
	          submittedAt
	        }
	      }
	    }
	  }
	}`

	var resp reviewStatusResponse
	vars := map[string]interface{}{
		"owner":  owner,
		"repo":   repo,
		"number": number,
	}
	if err := client.Do(query, vars, &resp); err != nil {
		return reviewStatus{}, fmt.Errorf("query review status: %w", err)
	}

	status := reviewStatus{}
	for _, node := range resp.Repository.PullRequest.ReviewRequests.Nodes {
		if strings.Contains(strings.ToLower(node.RequestedReviewer.Login), copilotRequestLogin) {
			status.CopilotRequested = true
			break
		}
	}

	var reviews []latestReview
	for _, node := range resp.Repository.PullRequest.LatestReviews.Nodes {
		if strings.Contains(strings.ToLower(node.Author.Login), copilotReviewLogin) {
			reviews = append(reviews, latestReview{
				State:       node.State,
				SubmittedAt: node.SubmittedAt,
			})
		}
	}
	sort.Slice(reviews, func(i, j int) bool {
		return reviews[i].SubmittedAt.Before(reviews[j].SubmittedAt)
	})
	if len(reviews) > 0 {
		status.LatestCopilotReview = &reviews[len(reviews)-1]
	}

	return status, nil
}

type requestedReviewerUser struct {
	Login string `json:"login"`
}

type requestedReviewersResponse struct {
	Users []requestedReviewerUser `json:"users"`
}

type pullRequestReviewUser struct {
	Login string `json:"login"`
}

type pullRequestReview struct {
	User        pullRequestReviewUser `json:"user"`
	State       string                `json:"state"`
	SubmittedAt time.Time             `json:"submitted_at"`
	CommitID    string                `json:"commit_id"`
}

func fetchReviewStatusREST(client *api.RESTClient, owner, repo string, number int) (reviewStatus, error) {
	var requestedReviewers requestedReviewersResponse
	if _, err := getRESTJSON(client, fmt.Sprintf("repos/%s/%s/pulls/%d/requested_reviewers", owner, repo, number), &requestedReviewers); err != nil {
		return reviewStatus{}, fmt.Errorf("query requested reviewers: %w", err)
	}

	reviews, err := fetchPullRequestReviewsREST(client, owner, repo, number)
	if err != nil {
		return reviewStatus{}, err
	}

	return buildReviewStatusFromREST(requestedReviewers, reviews), nil
}

func fetchPullRequestReviewsREST(client restRequester, owner, repo string, number int) ([]pullRequestReview, error) {
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews?per_page=%d", owner, repo, number, pullRequestReviewsPerPage)
	var reviews []pullRequestReview
	headers, err := getRESTJSON(client, path, &reviews)
	if err != nil {
		return nil, fmt.Errorf("query pull request reviews: %w", err)
	}

	lastPath, ok, err := lastPagePath(headers.Values("Link"))
	if err != nil {
		return nil, err
	}
	if !ok || lastPath == path {
		return reviews, nil
	}

	var latestPage []pullRequestReview
	if _, err := getRESTJSON(client, lastPath, &latestPage); err != nil {
		return nil, fmt.Errorf("query last pull request review page: %w", err)
	}
	return latestPage, nil
}

func buildReviewStatusFromREST(requestedReviewers requestedReviewersResponse, reviews []pullRequestReview) reviewStatus {
	status := reviewStatus{}
	for _, reviewer := range requestedReviewers.Users {
		if strings.Contains(strings.ToLower(reviewer.Login), copilotRequestLogin) {
			status.CopilotRequested = true
			break
		}
	}

	var latest *latestReview
	for _, review := range reviews {
		if strings.Contains(strings.ToLower(review.User.Login), copilotReviewLogin) {
			candidate := latestReview{
				State:       review.State,
				SubmittedAt: review.SubmittedAt,
			}
			if latest == nil || latest.SubmittedAt.Before(candidate.SubmittedAt) {
				latest = &candidate
			}
		}
	}
	status.LatestCopilotReview = latest

	return status
}

func lastPagePath(linkHeaders []string) (string, bool, error) {
	for _, linkHeader := range linkHeaders {
		for _, part := range strings.Split(linkHeader, ",") {
			part = strings.TrimSpace(part)
			if !strings.Contains(part, `rel="last"`) {
				continue
			}

			start := strings.Index(part, "<")
			end := strings.Index(part, ">")
			if start == -1 || end == -1 || end <= start+1 {
				return "", false, fmt.Errorf("parse Link header: %q", linkHeader)
			}

			target, err := url.Parse(part[start+1 : end])
			if err != nil {
				return "", false, fmt.Errorf("parse Link target: %w", err)
			}

			path := strings.TrimPrefix(target.EscapedPath(), "/")
			if target.RawQuery != "" {
				path += "?" + target.RawQuery
			}
			return path, true, nil
		}
	}
	return "", false, nil
}

type rateLimitSnapshot struct {
	CoreRemaining    int
	GraphQLRemaining int
}

type rateLimitResponse struct {
	Resources struct {
		Core struct {
			Remaining int `json:"remaining"`
		} `json:"core"`
		GraphQL struct {
			Remaining int `json:"remaining"`
		} `json:"graphql"`
	} `json:"resources"`
}

func fetchRateLimitSnapshot(client restRequester) (rateLimitSnapshot, error) {
	var resp rateLimitResponse
	if _, err := getRESTJSON(client, "rate_limit", &resp); err != nil {
		return rateLimitSnapshot{}, fmt.Errorf("query rate limits: %w", err)
	}
	return rateLimitSnapshot{
		CoreRemaining:    resp.Resources.Core.Remaining,
		GraphQLRemaining: resp.Resources.GraphQL.Remaining,
	}, nil
}
