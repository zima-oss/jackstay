package review

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/chatevent"
	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/strictjson"
)

// How a background request ends (W-019's redesign of round 2): one record per request,
// `<store>/.ends/<task key>-<request id>.json`, written once, under the request's lock, before the
// request is removed. It holds the request (its sessions included) and its outcome — finished,
// refused, or lost when its process died first — and which sessions have been told. Every
// session's `review.finished` event is made from it, with one key per request and session, so a
// retry after any crash publishes the same event, and no two outcomes are ever told for one
// request. Recovery has one rule: a request that has an end is over.

// End is how one background request ended.
type End struct {
	Request   Request  `json:"request"`
	Lost      bool     `json:"lost,omitempty"` // its process died before it finished
	Status    int      `json:"status"`
	Outcome   string   `json:"outcome"`
	Next      string   `json:"next"`
	Folder    string   `json:"folder,omitempty"`
	Round     int      `json:"round,omitempty"`
	Ended     string   `json:"ended"`
	Delivered []string `json:"delivered,omitempty"` // the sessions whose event the chat has stored
}

func (store Store) endsPath() string { return filepath.Join(store.Root, ".ends") }

func (store Store) endPath(task, id string) string {
	return filepath.Join(store.endsPath(), taskKey(task)+"-"+id+".json")
}

func readEnd(path string) (End, error) {
	data, err := durable.ReadLimited(path, maximumRequestBytes)
	if err != nil {
		return End{}, err
	}
	var end End
	if err := strictjson.UnmarshalOpen(data, &end); err != nil {
		return End{}, err
	}
	return end, nil
}

// endRequest ends the task's request when match says it is the one: under the request's lock it
// writes the request's end (made by build, unless the request has one already: a crash after
// writing it), then removes the request. found is false when no request matched.
func (store Store) endRequest(ctx context.Context, task string, match func(Request) bool, build func(Request) End) (End, bool, error) {
	path := store.RequestPath(task)
	held, err := durable.Lock(ctx, path)
	if err != nil {
		return End{}, false, err
	}
	defer held.Release()
	data, err := durable.ReadLimited(path, maximumRequestBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return End{}, false, nil
	}
	if err != nil {
		return End{}, false, err
	}
	var request Request
	if err := strictjson.UnmarshalOpen(data, &request); err != nil {
		return End{}, false, err
	}
	if !match(request) {
		return End{}, false, nil
	}
	endPath := store.endPath(task, request.ID)
	end, err := readEnd(endPath)
	if errors.Is(err, fs.ErrNotExist) {
		end = build(request)
		end.Request, end.Outcome = request, OutcomeWord(end.Status)
		if end.Lost {
			end.Outcome = "lost"
		}
		if end.Ended == "" {
			end.Ended = time.Now().UTC().Format(time.RFC3339Nano)
		}
		var encoded []byte
		if encoded, err = json.MarshalIndent(end, "", "  "); err == nil {
			if err = durable.MakeFolder(store.endsPath(), 0o700); err == nil {
				err = durable.WriteAtomic(endPath, encoded, 0o600, maximumRequestBytes)
			}
		}
	}
	if err != nil {
		return End{}, false, err
	}
	if err := os.Remove(path); err != nil {
		return End{}, false, err
	}
	if err := durable.SyncFolder(filepath.Dir(path)); err != nil {
		return End{}, false, err
	}
	store.forgetDeliveredEnds(task, request.ID)
	return end, true, nil
}

// Finish ends the run of request id with its result and tells its sessions what the chat takes;
// the rest is pulled later (DeliverOwed). It never fails the review: what it could not do is said.
func (store Store) Finish(ctx context.Context, task, id string, result Result, publisher chatevent.Publisher) error {
	end, found, err := store.endRequest(ctx, task, func(request Request) bool { return request.ID == id }, func(Request) End {
		end := End{Status: result.Status, Next: result.Next, Folder: result.Folder}
		if result.Record != nil {
			end.Round = result.Record.Round
		}
		return end
	})
	if err != nil || !found {
		return err
	}
	return store.deliver(ctx, end, "", publisher)
}

// endIfDead ends the task's request as lost when its process is gone.
func (background Background) endIfDead(ctx context.Context, task string) error {
	end, found, err := background.Store.endRequest(ctx, task, func(request Request) bool { return !background.live(request) },
		func(request Request) End {
			return End{Lost: true, Status: ExitReviewer,
				Next: fmt.Sprintf("its process is gone (the log is %s); submit it again: reviewers that answered are not run again", request.Log)}
		})
	if err != nil || !found {
		return err
	}
	// The end is saved: telling now is a courtesy, and what the chat does not take is pulled later.
	_ = background.Store.deliver(ctx, end, "", chatevent.Default())
	return nil
}

// ReapLost ends every request whose process died before it finished, so its sessions are told
// without waiting for another submission.
func (background Background) ReapLost(ctx context.Context) error {
	listed, err := os.ReadDir(background.Store.requestsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, item := range listed {
		if !strings.HasSuffix(item.Name(), ".json") || strings.HasPrefix(item.Name(), ".") {
			continue
		}
		data, err := durable.ReadLimited(filepath.Join(background.Store.requestsPath(), item.Name()), maximumRequestBytes)
		var request Request
		if err != nil || strictjson.UnmarshalOpen(data, &request) != nil || request.Task == "" || background.live(request) {
			continue
		}
		if err := background.endIfDead(ctx, request.Task); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// event is the `review.finished` event of end for one session.
func (end End) event(subscriber string) chatevent.Event {
	request := end.Request
	summary := fmt.Sprintf("review of %s (%s): %s", request.Task, request.rangeText(), end.Outcome)
	meta := map[string]string{"exit": strconv.Itoa(end.Status), "verdict": end.Outcome, "task": request.Task, "request": request.ID}
	var attachments []chatevent.Attachment
	if end.Lost {
		summary = fmt.Sprintf("review of %s (%s): stopped before it finished", request.Task, request.rangeText())
	} else if end.Round > 0 {
		summary = fmt.Sprintf("review of %s, round %d (%s): %s", request.Task, end.Round, request.rangeText(), end.Outcome)
		meta["round"] = strconv.Itoa(end.Round)
		attachments = append(attachments, chatevent.Attachment{URI: fmt.Sprintf("jst:review/%s/%d", request.Task, end.Round),
			Title: "the round's report"})
	}
	body := "next: " + end.Next
	if end.Folder != "" {
		body += "\nreport: " + filepath.Join(end.Folder, "report.md")
	}
	body += fmt.Sprintf("\nread it with `jst review last --task %s`", request.Task)
	if len(body) > chatevent.MaxBodyBytes {
		body = strings.ToValidUTF8(body[:chatevent.MaxBodyBytes], "")
	}
	digest := sha256.Sum256([]byte(request.ID + "\x00" + subscriber))
	return chatevent.Event{Kind: chatevent.KindReviewFinished, Worktree: request.Worktree, Summary: chatevent.Summary(summary),
		Body: body, Meta: meta, Attachments: attachments, Recipients: []string{subscriber},
		Key: "review/" + hex.EncodeToString(digest[:16])}
}

// deliver publishes end's event to each session not yet told (only to recipient, when given), and
// records each one the chat stored.
func (store Store) deliver(ctx context.Context, end End, recipient string, publisher chatevent.Publisher) error {
	var failures []error
	for _, subscriber := range end.Request.Subscribers {
		if recipient != "" && subscriber != recipient || slices.Contains(end.Delivered, subscriber) {
			continue
		}
		if err := publisher.Publish(ctx, end.event(subscriber)); err != nil {
			failures = append(failures, err)
			continue
		}
		if err := store.markDelivered(ctx, end, subscriber); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (store Store) markDelivered(ctx context.Context, end End, subscriber string) error {
	return durable.Update(ctx, store.endPath(end.Request.Task, end.Request.ID), 0o600, maximumRequestBytes, func(current []byte) ([]byte, error) {
		var saved End
		if current == nil || strictjson.UnmarshalOpen(current, &saved) != nil || slices.Contains(saved.Delivered, subscriber) {
			return nil, nil
		}
		saved.Delivered = append(saved.Delivered, subscriber)
		return json.MarshalIndent(saved, "", "  ")
	})
}

// ends are the store's ends, of one task when task is not "".
func (store Store) ends(task string) ([]End, error) {
	listed, err := os.ReadDir(store.endsPath())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	prefix := ""
	if task != "" {
		prefix = taskKey(task) + "-"
	}
	var found []End
	for _, item := range listed {
		name := item.Name()
		if !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") || !strings.HasPrefix(name, prefix) {
			continue
		}
		if end, err := readEnd(filepath.Join(store.endsPath(), name)); err == nil {
			found = append(found, end)
		}
	}
	slices.SortFunc(found, func(first, second End) int { return strings.Compare(first.Ended, second.Ended) })
	return found, nil
}

// LatestEnd is how the task's latest background request ended, if one has.
func (store Store) LatestEnd(task string) (End, bool, error) {
	found, err := store.ends(task)
	if err != nil || len(found) == 0 {
		return End{}, false, err
	}
	return found[len(found)-1], true, nil
}

// forgetDeliveredEnds removes the task's older ends whose sessions have all been told.
func (store Store) forgetDeliveredEnds(task, keep string) {
	found, _ := store.ends(task)
	for _, end := range found {
		if end.Request.ID == keep {
			continue
		}
		told := true
		for _, subscriber := range end.Request.Subscribers {
			told = told && slices.Contains(end.Delivered, subscriber)
		}
		if told {
			_ = os.Remove(store.endPath(task, end.Request.ID))
		}
	}
}

// Owed are the events not yet stored by the chat for recipient.
func (store Store) Owed(recipient string) ([]chatevent.Event, error) {
	found, err := store.ends("")
	if err != nil {
		return nil, err
	}
	var owed []chatevent.Event
	for _, end := range found {
		if slices.Contains(end.Request.Subscribers, recipient) && !slices.Contains(end.Delivered, recipient) {
			owed = append(owed, end.event(recipient))
		}
	}
	return owed, nil
}

// DeliverOwed tells recipient what it is owed, after ending the requests that died: the chat pulls
// it when that session next looks, so no event waits for another review to run.
func (background Background) DeliverOwed(ctx context.Context, recipient string, publisher chatevent.Publisher) error {
	failures := []error{background.ReapLost(ctx)}
	found, err := background.Store.ends("")
	failures = append(failures, err)
	for _, end := range found {
		if slices.Contains(end.Request.Subscribers, recipient) && !slices.Contains(end.Delivered, recipient) {
			failures = append(failures, background.Store.deliver(ctx, end, recipient, publisher))
		}
	}
	return errors.Join(failures...)
}
