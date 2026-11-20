package status

import (
	"fmt"
	"io"
	"strings"
	"time"
)

// writeText prints the report for a person: a block per repository in detail, else a line each.
func writeText(out io.Writer, report Report, now time.Time) {
	for _, repository := range report.Repositories {
		if report.Kind == KindRepository {
			writeDetail(out, repository, report, now)
		} else {
			writeRow(out, repository)
		}
	}
}

func writeRow(out io.Writer, repository Repository) {
	name := repository.Project
	if name == "" {
		name = "?"
	}
	fmt.Fprintf(out, "%s (%s): ", repository.Path, name)
	if repository.Status != "ok" && repository.RepositoryID == "" {
		fmt.Fprintf(out, "unknown: %s\n", repository.Error)
		return
	}
	counts := repository.Counts
	fmt.Fprintf(out, "worktrees %d, sessions %d, events %d; brief %s; chat %s; background %s; gate %s; receipt %s; cards %s\n",
		counts.Worktrees, counts.Sessions, counts.Events+counts.OmittedEvents, briefText(repository.Brief), chatText(repository.Chat),
		backgroundText(repository.Background), runText(repository.Gate), runText(repository.Receipt), cardsText(repository.Cards))
	writeErrors(out, repository)
}

func writeDetail(out io.Writer, repository Repository, report Report, now time.Time) {
	if repository.RepositoryID == "" {
		fmt.Fprintf(out, "%s: unknown: %s\n", repository.Path, repository.Error)
		return
	}
	name := repository.Project
	if name == "" {
		name = "(no jackstay.json)"
	}
	where := repository.Branch
	if where == "" && len(repository.Head) >= 12 {
		where = repository.Head[:12]
	}
	fmt.Fprintf(out, "%s at %s (%s)\n", name, repository.Path, where)
	branches := map[string]string{}
	labels := []string{}
	for _, tree := range repository.Worktrees {
		label := tree.Branch
		if label == "" {
			label = "detached"
		}
		if tree.Prunable {
			label += " (prunable)"
		}
		branches[tree.ID] = label
		labels = append(labels, label)
	}
	fmt.Fprintf(out, "  worktrees  %d: %s\n", len(repository.Worktrees), strings.Join(labels, ", "))
	window := report.Until.Sub(report.Since).Round(time.Minute)
	fmt.Fprintf(out, "  sessions   %d in the last %s\n", len(repository.Sessions), shortDuration(window))
	for _, seen := range repository.Sessions {
		id := seen.ID
		if len(id) > 12 {
			id = id[:12]
		}
		fmt.Fprintf(out, "    %-11s %-12s in %s, %s ago, guard %s\n", seen.Runner, id, branchOf(branches, seen.Worktree),
			shortDuration(now.Sub(seen.SeenAt)), seen.Guard)
	}
	fmt.Fprintf(out, "  events     %d", len(repository.Events)+repository.Counts.OmittedEvents)
	if repository.Counts.OmittedEvents > 0 {
		fmt.Fprintf(out, " (the newest %d)", len(repository.Events))
	}
	fmt.Fprintln(out)
	for _, event := range repository.Events {
		fmt.Fprintf(out, "    %s  %-20s %-10s %s\n", event.At.Local().Format("01-02 15:04"), event.Stage, event.Status,
			branchOf(branches, event.WorktreeID))
	}
	if repository.Journal.Status != "complete" {
		fmt.Fprintf(out, "  journal    %s: %d unreadable line(s), %d read problem(s)\n", repository.Journal.Status,
			repository.Journal.Unreadable, repository.Journal.Problems)
	}
	fmt.Fprintf(out, "  brief      %s\n", briefText(repository.Brief))
	fmt.Fprintf(out, "  chat       %s\n", chatText(repository.Chat))
	fmt.Fprintf(out, "  background %s\n", backgroundText(repository.Background))
	for _, job := range repository.Background.Jobs {
		label := job.Name
		if label == "" {
			label = job.ID
		}
		fmt.Fprintf(out, "    %s %s\n", label, job.State)
	}
	fmt.Fprintf(out, "  gate       %s\n", runText(repository.Gate))
	fmt.Fprintf(out, "  receipt    %s\n", runText(repository.Receipt))
	fmt.Fprintf(out, "  cards      %s\n", cardsText(repository.Cards))
	writeErrors(out, repository)
}

func writeErrors(out io.Writer, repository Repository) {
	for _, source := range []struct{ label, text string }{{"repository", repository.Error}, {"brief", repository.Brief.Error},
		{"chat", repository.Chat.Error}, {"background", repository.Background.Error}, {"gate", repository.Gate.Error},
		{"receipt", repository.Receipt.Error}, {"cards", repository.Cards.Error}} {
		if source.text != "" {
			fmt.Fprintf(out, "  %s: %s\n", source.label, source.text)
		}
	}
}

func branchOf(branches map[string]string, id string) string {
	if label, known := branches[id]; known {
		return label
	}
	return "?"
}

func briefText(brief BriefStatus) string {
	if brief.Status == "saved" {
		return fmt.Sprintf("%s (%s)", brief.TaskID, brief.Freshness)
	}
	return brief.Status
}

func chatText(chat ChatStatus) string {
	if chat.Status == "available" {
		return fmt.Sprintf("%d session(s), %d pending", chat.Sessions, chat.Pending)
	}
	return chat.Status
}

func backgroundText(jobs BackgroundStatus) string {
	text := jobs.Status
	if len(jobs.Jobs) > 0 {
		text = fmt.Sprintf("%d job(s)", len(jobs.Jobs))
	}
	if jobs.Elsewhere > 0 {
		text += fmt.Sprintf("; %d more entries saved on this machine (`jst procs --all`)", jobs.Elsewhere)
	}
	return text
}

func runText(run RunStatus) string {
	if run.Status == "recorded" {
		return fmt.Sprintf("%s, %s, %s", run.Outcome, run.Freshness, run.At)
	}
	return run.Status
}

func cardsText(cards CardStatus) string {
	if cards.Status != "ok" {
		return cards.Status
	}
	active := make([]string, 0, len(cards.Active))
	for _, card := range cards.Active {
		active = append(active, card.ID+" "+card.State)
	}
	if len(active) == 0 {
		return "none active"
	}
	return fmt.Sprintf("%d active (%s)", len(active), strings.Join(active, ", "))
}

// shortDuration is a duration in its largest whole unit: 45s, 12m, 3h, 2d.
func shortDuration(span time.Duration) string {
	switch {
	case span < time.Minute:
		return fmt.Sprintf("%ds", int(span.Seconds()))
	case span < time.Hour:
		return fmt.Sprintf("%dm", int(span.Minutes()))
	case span < 48*time.Hour:
		return fmt.Sprintf("%dh", int(span.Hours()))
	default:
		return fmt.Sprintf("%dd", int(span.Hours()/24))
	}
}
