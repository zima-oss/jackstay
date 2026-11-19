package mergequeue

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/google/go-github/v93/github"

	"github.com/zima-oss/jackstay/internal/config"
	"github.com/zima-oss/jackstay/internal/gh"
	"github.com/zima-oss/jackstay/internal/githubapi"
	"github.com/zima-oss/jackstay/internal/remote"
	"github.com/zima-oss/jackstay/internal/review"
)

// Provider contains only the read operations needed to validate a PR handoff.
type Provider interface {
	PullRequest(context.Context, int) (githubapi.PullRequest, error)
	TargetHead(context.Context, string) (string, error)
	RequiredChecks(context.Context, string) ([]gh.RequiredCheck, error)
	Checks(context.Context, string) ([]githubapi.Check, error)
	MergeCommit(context.Context, string) (MergeCommit, error)
	Reviews(context.Context, int) ([]Review, error)
	ReviewDecision(context.Context, int) (string, error)
}

// Review is one provider review and the commit it judged.
type Review struct {
	Author string
	State  string
	Commit string
}

// MergeCommit is the Git tree and ordered parent list of GitHub's test merge.
type MergeCommit struct {
	Tree    string
	Parents []string
}

type githubProvider struct{ *githubapi.Client }

func (provider githubProvider) TargetHead(ctx context.Context, branch string) (string, error) {
	ref, err := githubapi.Read(ctx, provider.Retry(), func() (*github.Reference, error) {
		ref, _, err := provider.API().Git.GetRef(ctx, provider.Owner(), provider.Name(), "heads/"+branch)
		return ref, err
	})
	if err != nil {
		return "", err
	}
	return ref.GetObject().GetSHA(), nil
}

func (provider githubProvider) ReviewDecision(ctx context.Context, number int) (string, error) {
	var data struct {
		Repository struct {
			PullRequest *struct {
				ReviewDecision string `json:"reviewDecision"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	err := provider.Query(ctx, `query($owner: String!, $name: String!, $number: Int!) {
  repository(owner: $owner, name: $name) { pullRequest(number: $number) { reviewDecision } }
}`, map[string]any{"owner": provider.Owner(), "name": provider.Name(), "number": number}, &data)
	if err != nil {
		return "", err
	}
	if data.Repository.PullRequest == nil {
		return "", errors.New("pull request was not returned")
	}
	return data.Repository.PullRequest.ReviewDecision, nil
}

func (provider githubProvider) RequiredChecks(ctx context.Context, branch string) ([]gh.RequiredCheck, error) {
	return gh.RequiredChecksFor(ctx, provider.Client, branch)
}

func (provider githubProvider) MergeCommit(ctx context.Context, sha string) (MergeCommit, error) {
	commit, err := githubapi.Read(ctx, provider.Retry(), func() (*github.Commit, error) {
		commit, _, err := provider.API().Git.GetCommit(ctx, provider.Owner(), provider.Name(), sha)
		return commit, err
	})
	if err != nil {
		return MergeCommit{}, err
	}
	result := MergeCommit{Tree: commit.GetTree().GetSHA()}
	for _, parent := range commit.GetParents() {
		result.Parents = append(result.Parents, parent.GetSHA())
	}
	return result, nil
}

func (provider githubProvider) Reviews(ctx context.Context, number int) ([]Review, error) {
	options := &github.ListOptions{PerPage: 100}
	var found []Review
	for {
		var response *github.Response
		page, err := githubapi.Read(ctx, provider.Retry(), func() ([]*github.PullRequestReview, error) {
			page, answer, err := provider.API().PullRequests.ListReviews(ctx, provider.Owner(), provider.Name(), number, options)
			response = answer
			return page, err
		})
		if err != nil {
			return nil, err
		}
		for _, item := range page {
			found = append(found, Review{Author: item.GetUser().GetLogin(), State: item.GetState(), Commit: item.GetCommitID()})
		}
		if response == nil || response.NextPage == 0 {
			return found, nil
		}
		options.Page = response.NextPage
	}
}

func (service Service) provider(ctx context.Context) (Provider, string, error) {
	if service.Provider != nil {
		return service.Provider, "", nil
	}
	_, loaded, err := config.Load(service.Here, []string{"github"})
	if err != nil {
		return nil, "", err
	}
	repository, err := remote.RepositoryOf(ctx, service.git(), loaded.Section("github"))
	if err != nil {
		return nil, "", err
	}
	account, _ := loaded.Section("github")["account"].(string)
	client, _, err := githubapi.Connect(ctx, githubapi.OSEnvironment(), account, repository.String())
	if err != nil {
		return nil, "", err
	}
	return githubProvider{client}, repository.String(), nil
}

func (service Service) verifyPolicy(ctx context.Context, state *State) error {
	if service.ReviewTask != "" {
		_, loaded, err := config.Load(service.Here, []string{"review"})
		if err != nil {
			return fmt.Errorf("review configuration: %w", err)
		}
		settings, problems := review.SettingsOf(loaded)
		if len(problems) > 0 {
			return fmt.Errorf("review configuration: %s", strings.Join(problems, "; "))
		}
		store, err := review.StoreFor(settings, service.Project)
		if err != nil {
			return err
		}
		reasons, _ := review.CheckReasons(settings, store, service.ReviewTask, state.Base, state.Head)
		if len(reasons) > 0 {
			return fmt.Errorf("review is not current and clear: %s", strings.Join(reasons, "; "))
		}
	}
	if state.PullRequest == 0 {
		return nil
	}
	provider, repository, err := service.provider(ctx)
	if err != nil {
		return fmt.Errorf("GitHub freshness cannot be checked: %w", err)
	}
	pull, err := provider.PullRequest(ctx, state.PullRequest)
	if err != nil {
		return fmt.Errorf("pull request #%d: %w", state.PullRequest, err)
	}
	if !strings.HasPrefix(state.BaseRef, "refs/heads/") {
		return errors.New("PR handoff needs a local base branch ref")
	}
	branch := strings.TrimPrefix(state.BaseRef, "refs/heads/")
	if pull.Number != state.PullRequest || pull.State != "open" || pull.Merged || pull.Draft ||
		pull.HeadSHA != state.Head || pull.Base != branch ||
		(reposDiffer(repository, pull.BaseRepository) && repository != "") {
		return fmt.Errorf("pull request #%d no longer matches the tested head, base, or open state", state.PullRequest)
	}
	liveBase, err := provider.TargetHead(ctx, branch)
	if err != nil || liveBase != state.Base {
		return fmt.Errorf("target branch moved from the tested base or cannot be read: %v", err)
	}
	if pull.MergeCommitSHA == "" {
		return errors.New("GitHub has no current test merge commit")
	}
	mergeCommit, err := provider.MergeCommit(ctx, pull.MergeCommitSHA)
	if err != nil || mergeCommit.Tree != state.Tree ||
		!slices.Equal(mergeCommit.Parents, []string{state.Base, state.Head}) {
		return fmt.Errorf("GitHub test merge tree or parents differ from the tested candidate or cannot be read: %v", err)
	}
	required, err := provider.RequiredChecks(ctx, branch)
	if err != nil {
		return fmt.Errorf("required-check policy cannot be read: %w", err)
	}
	if len(required) == 0 {
		return errors.New("the base branch has no readable required checks")
	}
	rows, err := provider.Checks(ctx, pull.MergeCommitSHA)
	if err != nil {
		return fmt.Errorf("test merge checks cannot be read: %w", err)
	}
	for _, check := range required {
		found := false
		for _, row := range rows {
			if row.Name != check.Name || (check.AppID != 0 && (row.Kind != githubapi.CheckRun || row.AppID != check.AppID)) {
				continue
			}
			if state := row.Bucket(); state != githubapi.Pass && state != githubapi.Skipping {
				return fmt.Errorf("required check %s is pending or failing on the test merge", check.String())
			}
			found = true
		}
		if !found {
			return fmt.Errorf("required check %s is missing from the test merge", check.String())
		}
	}
	for _, row := range rows {
		if slices.Contains([]githubapi.Bucket{githubapi.Fail, githubapi.Cancel}, row.Bucket()) {
			return fmt.Errorf("test merge check %s failed", row.Name)
		}
	}
	reviews, err := provider.Reviews(ctx, state.PullRequest)
	if err != nil {
		return fmt.Errorf("PR reviews cannot be read: %w", err)
	}
	latest := map[string]Review{}
	for _, item := range reviews {
		if item.Author != "" && (item.State == "APPROVED" || item.State == "CHANGES_REQUESTED" || item.State == "DISMISSED") {
			latest[strings.ToLower(item.Author)] = item
		}
	}
	approved := false
	for _, item := range latest {
		if strings.EqualFold(item.Author, pull.Author) {
			continue
		}
		if item.State == "CHANGES_REQUESTED" {
			return fmt.Errorf("PR review by %s requests changes", item.Author)
		}
		if item.State == "APPROVED" && item.Commit == state.Head {
			approved = true
		}
	}
	if !approved {
		return errors.New("the current PR head has no approval")
	}
	decision, err := provider.ReviewDecision(ctx, state.PullRequest)
	if err != nil || decision != "APPROVED" {
		return fmt.Errorf("GitHub's effective review decision is not approved: %q (%v)", decision, err)
	}
	confirmed, err := provider.PullRequest(ctx, state.PullRequest)
	if err != nil || confirmed.HeadSHA != pull.HeadSHA ||
		confirmed.MergeCommitSHA != pull.MergeCommitSHA || confirmed.Base != pull.Base || confirmed.State != pull.State ||
		confirmed.Draft != pull.Draft || confirmed.Merged != pull.Merged || confirmed.BaseRepository != pull.BaseRepository {
		return errors.New("pull request changed while its review and checks were read")
	}
	current, err := provider.RequiredChecks(ctx, branch)
	if err != nil || !slices.Equal(required, current) {
		return errors.New("required-check policy changed while the candidate was verified")
	}
	currentBase, err := provider.TargetHead(ctx, branch)
	if err != nil || currentBase != liveBase {
		return errors.New("target branch moved while review and checks were read")
	}
	return nil
}

func reposDiffer(expected, actual string) bool { return !strings.EqualFold(expected, actual) }
