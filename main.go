package main

import (
	"encoding/json"
	"fmt"
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
	copilotRequestLogin = "copilot"
	copilotReviewLogin  = "copilot-pull-request-reviewer"
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
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

	cmd := &cobra.Command{
		Use:   "request [<pr>]",
		Short: "Request or re-request review from Copilot on a pull request",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			selector := ""
			if len(args) == 1 {
				selector = args[0]
			}
			if wait {
				if err := validatePollingFlags(interval, timeout); err != nil {
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
				return pollReviewStatus(selector, interval, timeout, false)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&wait, "wait", false, "wait for Copilot review after requesting it")
	cmd.Flags().IntVar(&interval, "interval", 15, "poll interval in seconds when used with --wait")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "stop waiting after N seconds when used with --wait (0 disables timeout)")
	return cmd
}

func newCheckCmd() *cobra.Command {
	var interval int
	var timeout int
	var async bool

	cmd := &cobra.Command{
		Use:   "check [<pr>]",
		Short: "Check Copilot review status on a pull request",
		Long: "Poll until the Copilot review is no longer pending (default). " +
			"With --async, perform a single poll and return a non-zero exit status while a review is still requested.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Keep flag validation aligned with the help text: async mode ignores
			// polling-related flags entirely, including invalid values.
			if !async {
				if err := validatePollingFlags(interval, timeout); err != nil {
					return err
				}
			}

			selector := ""
			if len(args) == 1 {
				selector = args[0]
			}
			return pollReviewStatus(selector, interval, timeout, async)
		},
	}

	cmd.Flags().IntVar(&interval, "interval", 15, "poll interval in seconds when waiting; ignored with --async")
	cmd.Flags().IntVar(&timeout, "timeout", 0, "stop waiting after N seconds (0 disables timeout); ignored with --async")
	cmd.Flags().BoolVar(&async, "async", false, "perform a single check and exit immediately; returns non-zero while review is still pending")
	return cmd
}

func pollReviewStatus(selector string, interval, timeout int, async bool) error {
	target, err := resolvePR(selector)
	if err != nil {
		return err
	}
	repo, err := repository.Current()
	if err != nil {
		return fmt.Errorf("resolve current repository: %w", err)
	}
	client, err := api.DefaultGraphQLClient()
	if err != nil {
		return fmt.Errorf("build GraphQL client: %w", err)
	}

	deadline := time.Time{}
	if timeout > 0 {
		deadline = time.Now().Add(time.Duration(timeout) * time.Second)
	}

	for {
		status, err := fetchReviewStatus(client, repo.Owner, repo.Name, target.Number)
		if err != nil {
			return err
		}

		if status.CopilotRequested {
			if async {
				return fmt.Errorf("Copilot review is still pending on %s", target.URL)
			}
			fmt.Printf("%s awaiting review from Copilot on %s\n", time.Now().Format("2006-01-02 15:04:05"), target.URL)
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

func validatePollingFlags(interval, timeout int) error {
	if interval < 1 {
		return fmt.Errorf("interval must be positive: %d", interval)
	}
	if timeout < 0 {
		return fmt.Errorf("timeout must be non-negative: %d", timeout)
	}
	return nil
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

func fetchReviewStatus(client *api.GraphQLClient, owner, repo string, number int) (reviewStatus, error) {
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
