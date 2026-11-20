package gh

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/zima-oss/jackstay/internal/githubapi"
)

// serveGitHub is a GitHub API that answers each path with its JSON (404 for any other).
func serveGitHub(t *testing.T, answers map[string]string) client {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, found := answers[request.URL.Path]
		writer.Header().Set("Content-Type", "application/json")
		if !found {
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"message": "Not Found"}`)
			return
		}
		_, _ = io.WriteString(writer, body)
	}))
	t.Cleanup(server.Close)
	connected, err := githubapi.New("token", "o/p", githubapi.WithBaseURL(server.URL+"/"), githubapi.WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	return client{Client: connected}
}

func TestBranchRulesReadRulesetsAndClassicProtection(t *testing.T) {
	api := serveGitHub(t, map[string]string{
		"/repos/o/p/rules/branches/main": `[
			{"type": "required_status_checks", "parameters": {"strict_required_status_checks_policy": false,
				"required_status_checks": [{"context": "gate", "integration_id": 15368}]}},
			{"type": "merge_queue", "parameters": {"merge_method": "SQUASH", "grouping_strategy": "ALLGREEN"}}]`,
		"/repos/o/p/branches/main": `{"name": "main", "protected": true,
			"protection": {"required_status_checks": {"strict": true, "contexts": ["lint", "gate"]}}}`,
	})
	rules, err := api.BranchRules(context.Background(), "main")
	if err != nil {
		t.Fatal(err)
	}
	if want := []RequiredCheck{{Name: "gate"}, {Name: "gate", AppID: 15368}, {Name: "lint"}}; !slices.Equal(rules.Required, want) {
		t.Fatalf("required %+v, want %+v", rules.Required, want)
	}
	if !rules.UpToDate || !rules.Queue {
		t.Fatalf("classic strict and a merge queue rule: %+v", rules)
	}
}

func TestBranchRulesStrictFromARulesetAndNoQueue(t *testing.T) {
	api := serveGitHub(t, map[string]string{
		"/repos/o/p/rules/branches/main": `[{"type": "required_status_checks", "parameters": {"strict_required_status_checks_policy": true,
			"required_status_checks": [{"context": "gate"}]}}]`,
		"/repos/o/p/branches/main": `{"name": "main", "protected": false}`,
	})
	rules, err := api.BranchRules(context.Background(), "main")
	if err != nil || !rules.UpToDate || rules.Queue || !slices.Equal(rules.Required, []RequiredCheck{{Name: "gate"}}) {
		t.Fatalf("%+v %v", rules, err)
	}
}

func TestBranchRulesARefusalKeepsItsStatus(t *testing.T) {
	api := serveGitHub(t, map[string]string{})
	_, err := api.BranchRules(context.Background(), "main")
	if githubapi.StatusOf(err) != http.StatusNotFound || !refused(err) {
		t.Fatalf("a 404 is a refusal the adapter can tell: %v", err)
	}
}

func TestMergeMethodsAreTheRepositorysAllowances(t *testing.T) {
	api := serveGitHub(t, map[string]string{
		"/repos/o/p": `{"full_name": "o/p", "allow_squash_merge": true, "allow_merge_commit": false, "allow_rebase_merge": true}`,
	})
	methods, read, err := api.MergeMethods(context.Background())
	if err != nil || !read || !slices.Equal(methods, []string{"squash", "rebase"}) {
		t.Fatalf("%v %v %v", methods, read, err)
	}
	hidden := serveGitHub(t, map[string]string{"/repos/o/p": `{"full_name": "o/p"}`})
	methods, read, err = hidden.MergeMethods(context.Background())
	if err != nil || read || methods != nil {
		t.Fatalf("not shown: %v %v %v", methods, read, err)
	}
}
