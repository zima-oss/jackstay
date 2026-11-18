package githubapi

import (
	"context"
	"fmt"
	"time"
)

// CheckKind is what a check is: a check run (an app's, GitHub Actions' among them) or a commit
// status.
type CheckKind string

// The two kinds of a statusCheckRollup context.
const (
	CheckRun      CheckKind = "CheckRun"
	StatusContext CheckKind = "StatusContext"
)

// Check is one context of a commit's statusCheckRollup: the newest run of each check name, and
// the commit statuses of other apps.
type Check struct {
	Name       string
	Kind       CheckKind
	Status     string // a check run's: QUEUED, IN_PROGRESS, COMPLETED, …
	Conclusion string // a completed check run's: SUCCESS, FAILURE, …
	State      string // a status's: SUCCESS, PENDING, FAILURE, ERROR, EXPECTED
	Link       string
	Workflow   string // the workflow of an Actions check run
	WorkflowID int64  // its workflow's id
	RunID      int64  // its workflow run
	Event      string // the event that started the run: push, pull_request, …
	Branch     string // the branch its check suite ran for
	// Annotations are a check run's first annotations' messages: GitHub writes why it cancelled a
	// run there ("Canceling since a higher priority waiting request for 'GROUP' exists").
	Annotations []string
	AppID       int64  // the app whose check suite holds a check run (GitHub Actions: 15368)
	Description string // a status's
	StartedAt   time.Time
	CompletedAt time.Time
}

// Bucket is where a check stands, as `gh pr checks` sorts them.
type Bucket string

// The buckets of docs/design/implementation.md, section 8.1.
const (
	Pass     Bucket = "pass"
	Fail     Bucket = "fail"
	Pending  Bucket = "pending"
	Skipping Bucket = "skipping"
	Cancel   Bucket = "cancel" // settled, and failing
)

// BucketOf is the bucket of a check's state: a completed check run's conclusion or a status's
// state.
func BucketOf(state string) Bucket {
	switch state {
	case "SUCCESS":
		return Pass
	case "SKIPPED", "NEUTRAL":
		return Skipping
	case "FAILURE", "ERROR", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
		return Fail
	case "CANCELLED":
		return Cancel
	}
	return Pending // STALE, PENDING, EXPECTED, a run not completed, anything new
}

// Bucket is the check's bucket: a status by its state, a check run by its conclusion once it has
// completed, pending before.
func (check Check) Bucket() Bucket {
	if check.Kind == StatusContext {
		return BucketOf(check.State)
	}
	if check.Status != "COMPLETED" {
		return Pending
	}
	return BucketOf(check.Conclusion)
}

const checksQuery = `query($owner: String!, $name: String!, $oid: GitObjectID!, $after: String) {
  repository(owner: $owner, name: $name) {
    object(oid: $oid) { ... on Commit {
      statusCheckRollup { contexts(first: 100, after: $after) {
        pageInfo { hasNextPage endCursor }
        nodes {
          __typename
          ... on CheckRun { name status conclusion detailsUrl startedAt completedAt
            annotations(first: 10) { nodes { message } }
            checkSuite { app { databaseId } branch { name } workflowRun { databaseId event workflow { databaseId name } } } }
          ... on StatusContext { context state targetUrl description createdAt }
        }
      } }
    } }
  }
}`

type checkNode struct {
	Typename    string     `json:"__typename"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  string     `json:"conclusion"`
	DetailsURL  string     `json:"detailsUrl"`
	StartedAt   *time.Time `json:"startedAt"`
	CompletedAt *time.Time `json:"completedAt"`
	Annotations *struct {
		Nodes []struct {
			Message string `json:"message"`
		} `json:"nodes"`
	} `json:"annotations"`
	CheckSuite *struct {
		App *struct {
			DatabaseID int64 `json:"databaseId"`
		} `json:"app"`
		Branch *struct {
			Name string `json:"name"`
		} `json:"branch"`
		WorkflowRun *struct {
			DatabaseID int64  `json:"databaseId"`
			Event      string `json:"event"`
			Workflow   struct {
				DatabaseID int64  `json:"databaseId"`
				Name       string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	Context     string     `json:"context"`
	State       string     `json:"state"`
	TargetURL   string     `json:"targetUrl"`
	Description string     `json:"description"`
	CreatedAt   *time.Time `json:"createdAt"`
}

func (node checkNode) check() Check {
	if node.Typename == string(StatusContext) {
		check := Check{Name: node.Context, Kind: StatusContext, State: node.State, Link: node.TargetURL, Description: node.Description}
		if node.CreatedAt != nil {
			check.StartedAt = *node.CreatedAt
		}
		return check
	}
	check := Check{Name: node.Name, Kind: CheckRun, Status: node.Status, Conclusion: node.Conclusion, Link: node.DetailsURL}
	if node.StartedAt != nil {
		check.StartedAt = *node.StartedAt
	}
	if node.CompletedAt != nil {
		check.CompletedAt = *node.CompletedAt
	}
	if node.Annotations != nil {
		for _, annotation := range node.Annotations.Nodes {
			check.Annotations = append(check.Annotations, annotation.Message)
		}
	}
	if node.CheckSuite != nil && node.CheckSuite.App != nil {
		check.AppID = node.CheckSuite.App.DatabaseID
	}
	if node.CheckSuite != nil && node.CheckSuite.Branch != nil {
		check.Branch = node.CheckSuite.Branch.Name
	}
	if node.CheckSuite != nil && node.CheckSuite.WorkflowRun != nil {
		check.Workflow = node.CheckSuite.WorkflowRun.Workflow.Name
		check.WorkflowID = node.CheckSuite.WorkflowRun.Workflow.DatabaseID
		check.RunID = node.CheckSuite.WorkflowRun.DatabaseID
		check.Event = node.CheckSuite.WorkflowRun.Event
	}
	return check
}

// CommitNotFoundError is a commit the repository does not have.
type CommitNotFoundError struct{ SHA string }

func (err *CommitNotFoundError) Error() string {
	return fmt.Sprintf("GitHub has no commit %s in this repository", err.SHA)
}

// Checks is a commit's checks: one GraphQL query of its statusCheckRollup, paged to the end (what
// `gh pr checks` reads). None registered is an empty list: waiting, for the caller.
func (client *Client) Checks(ctx context.Context, sha string) ([]Check, error) {
	variables := map[string]any{"owner": client.owner, "name": client.name, "oid": sha, "after": nil}
	var out []Check
	for {
		var data struct {
			Repository struct {
				Object *struct {
					StatusCheckRollup *struct {
						Contexts struct {
							PageInfo pageInfo    `json:"pageInfo"`
							Nodes    []checkNode `json:"nodes"`
						} `json:"contexts"`
					} `json:"statusCheckRollup"`
				} `json:"object"`
			} `json:"repository"`
		}
		if err := client.Query(ctx, checksQuery, variables, &data); err != nil {
			return nil, err
		}
		if data.Repository.Object == nil {
			return nil, &CommitNotFoundError{SHA: sha}
		}
		rollup := data.Repository.Object.StatusCheckRollup
		if rollup == nil {
			return out, nil
		}
		for _, node := range rollup.Contexts.Nodes {
			out = append(out, node.check())
		}
		if !rollup.Contexts.PageInfo.HasNextPage {
			return out, nil
		}
		variables["after"] = rollup.Contexts.PageInfo.EndCursor
	}
}
