package chatstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zima-oss/jackstay/internal/durable"
	"github.com/zima-oss/jackstay/internal/sessionchat/deliverystate"
)

// clock is a test's time, moved by hand.
type clock struct{ at time.Time }

func (moment *clock) now() time.Time { return moment.at }

func (moment *clock) advance(by time.Duration) { moment.at = moment.at.Add(by) }

type fixture struct {
	t     *testing.T
	store *Store
	clock *clock
	ctx   context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	moment := &clock{at: time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)}
	return &fixture{t: t, store: Open(filepath.Join(t.TempDir(), "v1"), Options{Now: moment.now}), clock: moment, ctx: context.Background()}
}

var (
	alpha = Channel{ID: strings.Repeat("a", 64), Name: "alpha"}
	beta  = Channel{ID: strings.Repeat("b", 64), Name: "beta"}
	gamma = Channel{ID: strings.Repeat("c", 64), Name: "gamma"}
)

// session registers a Claude Code session on the hooks route in a state.
func (f *fixture) session(key, name string, channel Channel, state string) Session {
	f.t.Helper()
	session, err := f.store.Register(f.ctx, Registration{
		Key: key, KeySource: SourceEngine, Engine: EngineClaudeCode, Name: name, Channel: channel,
		Worktree: "/work/" + name, Delivery: DeliveryHooks, State: state,
	})
	if err != nil {
		f.t.Fatalf("register %s: %v", key, err)
	}
	return session
}

func (f *fixture) send(from string, input SendInput) SendResult {
	f.t.Helper()
	result, err := f.store.Send(f.ctx, from, Scope{}, input)
	if err != nil {
		f.t.Fatalf("send from %s to %s: %v", from, input.To, err)
	}
	return result
}

func (f *fixture) refused(err error, reason string) {
	f.t.Helper()
	if ReasonOf(err) != reason {
		f.t.Fatalf("got %v, want refusal %s", err, reason)
	}
}

func (f *fixture) delivery(id, key string) Delivery {
	f.t.Helper()
	var delivery Delivery
	if err := readRecord(f.store.place.delivery(id, hashKey(key)), deliveryBound, &delivery); err != nil {
		f.t.Fatalf("delivery %s for %s: %v", id, key, err)
	}
	return delivery
}

func (f *fixture) commit(id string) Commit {
	f.t.Helper()
	var commit Commit
	if err := readRecord(f.store.place.commit(id), commitBound, &commit); err != nil {
		f.t.Fatalf("commit %s: %v", id, err)
	}
	return commit
}

func (f *fixture) record(key string) Session {
	f.t.Helper()
	var session Session
	if err := readRecord(f.store.place.session(hashKey(key)), sessionBound, &session); err != nil {
		f.t.Fatalf("session %s: %v", key, err)
	}
	return session
}

func TestMessageIDsCarryTheirTimeAndMatchThePatterns(t *testing.T) {
	moment := time.Date(2026, 11, 20, 10, 0, 0, 123_000_000, time.UTC)
	id, err := newMessageID(moment, strings.NewReader(strings.Repeat("\xff", 10)))
	if err != nil || !messageIDPattern.MatchString(id) {
		t.Fatalf("id %q, %v", id, err)
	}
	if got, ok := messageTime(id); !ok || !got.Equal(moment) {
		t.Fatalf("time %v, want %v", got, moment)
	}
	handle, err := newHandle(strings.NewReader(strings.Repeat("\xff", 8)))
	if err != nil || !handlePattern.MatchString(id+"."+handle) {
		t.Fatalf("handle %q, %v", handle, err)
	}
	if id, handle, ok := splitReference(id + "." + handle); !ok || handle == "" || !messageIDPattern.MatchString(id) {
		t.Fatalf("split %q %q %v", id, handle, ok)
	}
	request, err := newRequestID(strings.NewReader(strings.Repeat("\xff", 16)))
	if err != nil || !requestIDPattern.MatchString(request) {
		t.Fatalf("request ID %q, %v", request, err)
	}
}

func TestChannelNamesAreNormalised(t *testing.T) {
	cases := map[string]string{
		"My App":                       "my-app",
		"!!!":                          "repo",
		"common":                       "common-repo",
		"Common":                       "common-repo",
		"_web.":                        "web",
		strings.Repeat("ab", 30):       strings.Repeat("ab", 24),
		strings.Repeat("a", 47) + "-b": strings.Repeat("a", 47),
	}
	for source, want := range cases {
		if got := ChannelName(source); got != want {
			t.Errorf("ChannelName(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestRepositoryChannelNamesTheClonesFolder(t *testing.T) {
	root := filepath.Join(t.TempDir(), "My App")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	channel, err := RepositoryChannel(filepath.Join(root, ".git"), "")
	if err != nil || channel.Name != "my-app" || len(channel.ID) != 64 {
		t.Fatalf("%+v %v", channel, err)
	}
	named, err := RepositoryChannel(filepath.Join(root, ".git"), "Web")
	if err != nil || named.Name != "web" || named.ID != channel.ID {
		t.Fatalf("%+v %v", named, err)
	}
}

func TestAddressesParse(t *testing.T) {
	for text, want := range map[string]address{
		"web@fix":          {name: "web@fix"},
		"web@fix [3fa9c1]": {name: "web@fix", ref: "3fa9c1"},
		"[3fa9c1]":         {ref: "3fa9c1"},
		"main":             {parent: true},
		"#web":             {channel: true, name: "web"},
		"#web [3fa9c1ab]":  {channel: true, name: "web", ref: "3fa9c1ab"},
	} {
		got, err := parseAddress(text)
		if err != nil || got != want {
			t.Errorf("parseAddress(%q) = %+v, %v; want %+v", text, got, err, want)
		}
	}
	for _, text := range []string{"", "#Web", "a\nb", "#", strings.Repeat("x", 301), " x"} {
		if _, err := parseAddress(text); err == nil {
			t.Errorf("parseAddress(%q) accepted", text)
		}
	}
}

func TestRefsLengthenOnACollision(t *testing.T) {
	hash := "3fa9c1" + strings.Repeat("0", 58)
	if got := shortRef(hash, nil); got != "3fa9c1" {
		t.Fatal(got)
	}
	if got := shortRef(hash, []string{"3fa9c1" + strings.Repeat("1", 58)}); got != "3fa9c100" {
		t.Fatal(got)
	}
	if got := shortRef(hash, []string{"3fa9c100" + strings.Repeat("1", 56)}); got != "3fa9c10000" {
		t.Fatal(got)
	}
}

func TestRegistrationSubscribesTheSessionToItsOwnChannel(t *testing.T) {
	f := newFixture(t)
	session := f.session("cc:one", "", alpha, StateIdle)
	if session.Name != "alpha" || session.Ref != hashKey("cc:one")[:6] || session.State != StateIdle {
		t.Fatalf("%+v", session)
	}
	if len(session.Subscriptions) != 1 || session.Subscriptions[0] != alpha.ID {
		t.Fatalf("subscriptions %v", session.Subscriptions)
	}
	mailbox, err := f.store.Register(f.ctx, Registration{Key: "cx:thread", KeySource: SourceEngine, Engine: EngineCodex,
		Name: "review:task/r1/codex", Channel: alpha, Mailbox: true, State: StateIdle})
	if err != nil || len(mailbox.Subscriptions) != 0 {
		t.Fatalf("a mailbox subscribes to nothing: %+v %v", mailbox, err)
	}
	again := f.session("cc:one", "renamed", alpha, StateBusy)
	if again.Name != "renamed" || again.Registered != session.Registered || again.State != StateBusy {
		t.Fatalf("%+v", again)
	}
	if _, err := f.store.Register(f.ctx, Registration{Key: "nokey", KeySource: SourceEngine, Engine: EngineOther, Channel: alpha}); ReasonOf(err) != ReasonInvalid {
		t.Fatalf("a key without its engine prefix: %v", err)
	}
	if _, err := f.store.Rename(f.ctx, "cc:one", "a#b"); ReasonOf(err) != ReasonInvalid {
		t.Fatalf("rename with #: %v", err)
	}
}

func TestADirectSendIsStoredWholeWithItsMarkers(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	result := f.send("cc:a", SendInput{To: "b", Message: "first line\nsecond", Meta: map[string]string{"pr": "12"}})
	if result.Outcome != OutcomeAccepted || result.ThreadID != result.MessageID || len(result.Recipients) != 1 ||
		result.Recipients[0].State != "pending" || result.Recipients[0].Session != "b" {
		t.Fatalf("%+v", result)
	}
	var envelope Envelope
	if err := readRecord(f.store.place.envelope(result.MessageID), envelopeBound, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Summary != "first line" || envelope.Priority != "next" || envelope.To != "b ["+hashKey("cc:b")[:6]+"]" {
		t.Fatalf("%+v", envelope)
	}
	if created, _ := parseStamp(envelope.Created); !created.Add(72 * time.Hour).Equal(mustStamp(t, envelope.ExpiresAt)) {
		t.Fatalf("default expiry: %s %s", envelope.Created, envelope.ExpiresAt)
	}
	body, err := durable.ReadLimited(f.store.place.body(result.MessageID), MaxBodyBytes)
	if err != nil || string(body) != "first line\nsecond" {
		t.Fatalf("body %q %v", body, err)
	}
	if delivery := f.delivery(result.MessageID, "cc:b"); delivery.Route != "hooks" || delivery.State != "pending" || delivery.Handle != "" {
		t.Fatalf("%+v", delivery)
	}
	for _, marker := range []string{
		filepath.Join(f.store.place.inbox(hashKey("cc:b")), result.MessageID),
		filepath.Join(f.store.place.outbox(hashKey("cc:a")), result.MessageID),
	} {
		if !exists(marker) {
			t.Fatalf("missing %s", marker)
		}
	}
	if names, _ := listNames(f.store.place.journal()); len(names) != 0 {
		t.Fatalf("journal left: %v", names)
	}
	if f.record("cc:a").Exchange[hashKey("cc:b")] != 1 || f.record("cc:b").Exchange[hashKey("cc:a")] != 1 {
		t.Fatal("the pair's exchange is counted on both sides")
	}
}

func mustStamp(t *testing.T, text string) time.Time {
	t.Helper()
	moment, ok := parseStamp(text)
	if !ok {
		t.Fatalf("bad time %q", text)
	}
	return moment
}

func TestANativeDeliveryGetsItsHandle(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	if _, err := f.store.Register(f.ctx, Registration{Key: "cc:n", KeySource: SourceEngine, Engine: EngineClaudeCode,
		Name: "n", Channel: alpha, Delivery: DeliveryNative, State: StateIdle}); err != nil {
		t.Fatal(err)
	}
	result := f.send("cc:a", SendInput{To: "n", Message: "hello"})
	delivery := f.delivery(result.MessageID, "cc:n")
	if delivery.Route != "native" || len(delivery.Handle) != 13 {
		t.Fatalf("%+v", delivery)
	}
}

func TestAddressesResolveAndRefuse(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:twin1", "twin", alpha, StateIdle)
	twin2 := f.session("cc:twin2", "twin", alpha, StateIdle)
	f.session("cc:old", "old", alpha, StateGone)
	_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "twin", Message: "x"})
	f.refused(err, ReasonAmbiguousRecipient)
	if !strings.Contains(err.Error(), "twin ["+twin2.Ref+"]") {
		t.Fatalf("the detail lists name [ref]: %v", err)
	}
	if result := f.send("cc:a", SendInput{To: "twin [" + twin2.Ref + "]", Message: "x"}); result.Recipients[0].Session != "twin ["+twin2.Ref+"]" {
		t.Fatalf("%+v", result)
	}
	f.send("cc:a", SendInput{To: "[" + twin2.Ref + "]", Message: "y"})
	_, err = f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "old", Message: "x"})
	f.refused(err, ReasonRecipientGone)
	_, err = f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "nobody", Message: "x"})
	f.refused(err, ReasonUnknownRecipient)
	_, err = f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "main", Message: "x"})
	f.refused(err, ReasonNoParent)
	if _, err := f.store.Register(f.ctx, Registration{Key: "cc:child", KeySource: SourceEngine, Engine: EngineClaudeCode,
		Name: "child", Channel: alpha, Parent: "cc:a", State: StateIdle}); err != nil {
		t.Fatal(err)
	}
	if result := f.send("cc:child", SendInput{To: "main", Message: "done"}); result.Recipients[0].Session != "a" {
		t.Fatalf("%+v", result)
	}
	_, err = f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "#nowhere", Message: "x"})
	f.refused(err, ReasonUnknownRecipient)
	_, err = f.store.Send(f.ctx, "cc:unregistered", Scope{}, SendInput{To: "a", Message: "x"})
	f.refused(err, ReasonInvalid)
}

func TestAChannelReachesItsSubscribersAsTheyAreAtTheSend(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.session("cc:c", "c", beta, StateIdle)
	f.session("cc:gone", "gone", alpha, StateGone)
	result := f.send("cc:a", SendInput{To: "#alpha", Message: "to everyone here"})
	if len(result.Recipients) != 1 || result.Recipients[0].Session != "b" {
		t.Fatalf("subscribers but the sender and the gone: %+v", result)
	}
	empty := f.send("cc:c", SendInput{To: "#beta", Message: "history only"})
	if empty.Outcome != OutcomeAccepted || len(empty.Recipients) != 0 {
		t.Fatalf("a channel with no other subscriber keeps the message: %+v", empty)
	}
	if _, err := f.store.Subscribe(f.ctx, "cc:c", "#alpha", true); err != nil {
		t.Fatal(err)
	}
	later := f.send("cc:a", SendInput{To: "#alpha", Message: "again, to everyone here"})
	if len(later.Recipients) != 2 {
		t.Fatalf("a new subscriber hears later messages: %+v", later)
	}
	_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "#alpha", Message: "q", Request: &RequestInput{Expects: "decision"}})
	f.refused(err, ReasonRequestToChannel)
	_, err = f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "#alpha", NotifyWhenIdle: true})
	f.refused(err, ReasonRequestToChannel)
}

func TestTheRelatedListLimitsSendingOnly(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:a2", "a2", alpha, StateIdle)
	f.session("cc:b", "b", beta, StateIdle)
	f.session("cc:c", "c", gamma, StateIdle)
	f.session("cc:loose", "loose", Common(), StateIdle)
	f.session("cc:twin", "twin", Channel{ID: strings.Repeat("d", 64), Name: "beta"}, StateIdle)
	restricted := Scope{Restricted: true, Related: []string{"#gamma"}}
	cases := []struct {
		to     string
		scope  Scope
		reason string
	}{
		{"a2", restricted, ""},
		{"#alpha", restricted, ""},
		{"c", restricted, ""},
		{"#gamma", restricted, ""},
		{"b", restricted, ReasonOutOfScope},
		{"loose", restricted, ReasonOutOfScope},
		{"#common", restricted, ReasonOutOfScope},
		{"loose", Scope{Restricted: true, Related: []string{"#common"}}, ""},
		{"#common", Scope{Restricted: true, Related: []string{"#common"}}, ""},
		{"b", Scope{Restricted: true, Related: []string{"#beta"}}, ReasonOutOfScope}, // ambiguous: allows none
		{"b", Scope{Restricted: true, Related: []string{"#beta [" + beta.ID[:6] + "]"}}, ""},
		{"b", Scope{Restricted: true, Related: []string{"#delta"}}, ReasonOutOfScope},
		{"b", Scope{Restricted: true}, ReasonOutOfScope},
		{"b", Scope{}, ""},
	}
	for index, test := range cases {
		f.clock.advance(time.Minute) // keep clear of the rate and repeat limits
		_, err := f.store.Send(f.ctx, "cc:a", test.scope, SendInput{To: test.to, Message: "case " + string(rune('a'+index))})
		if ReasonOf(err) != test.reason || (test.reason == "" && err != nil) {
			t.Errorf("case %d (%s, %v): got %v, want %q", index, test.to, test.scope.Related, err, test.reason)
		}
	}
}

func TestAReplyMayGoBackToItsSenderOutsideTheList(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", beta, StateIdle)
	question := f.send("cc:b", SendInput{To: "a", Message: "ping"})
	restricted := Scope{Restricted: true}
	reply, err := f.store.Send(f.ctx, "cc:a", restricted, SendInput{To: "b", Message: "pong", ReplyTo: question.MessageID})
	if err != nil || reply.ThreadID != question.ThreadID {
		t.Fatalf("%+v %v", reply, err)
	}
	f.clock.advance(time.Minute)
	_, err = f.store.Send(f.ctx, "cc:a", restricted, SendInput{To: "b", Message: "unprompted"})
	f.refused(err, ReasonOutOfScope)
	_, err = f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "x", ReplyTo: question.MessageID, ThreadID: reply.MessageID})
	f.refused(err, ReasonInvalid)
	f.session("cc:c", "c", alpha, StateIdle)
	_, err = f.store.Send(f.ctx, "cc:c", Scope{}, SendInput{To: "b", Message: "x", ReplyTo: question.MessageID})
	f.refused(err, ReasonUnknownMessage)
}

func TestTheLimitsOfSection48(t *testing.T) {
	t.Run("rate", func(t *testing.T) {
		f := newFixture(t)
		f.session("cc:a", "a", alpha, StateIdle)
		f.session("cc:b", "b", alpha, StateIdle)
		f.session("cc:c", "c", alpha, StateIdle)
		for index := range sendsPerMinute {
			to := []string{"b", "c"}[index%2]
			f.send("cc:a", SendInput{To: to, Message: "n" + strings.Repeat("x", index)})
		}
		_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "one too many"})
		f.refused(err, ReasonRateLimited)
		f.clock.advance(61 * time.Second)
		f.send("cc:a", SendInput{To: "b", Message: "one too many"})
	})
	t.Run("repeat", func(t *testing.T) {
		f := newFixture(t)
		f.session("cc:a", "a", alpha, StateIdle)
		f.session("cc:b", "b", alpha, StateIdle)
		f.send("cc:a", SendInput{To: "b", Message: "same"})
		_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "same"})
		f.refused(err, ReasonRepeated)
		f.clock.advance(61 * time.Second)
		f.send("cc:a", SendInput{To: "b", Message: "same"})
	})
	t.Run("queue", func(t *testing.T) {
		f := newFixture(t)
		f.session("cc:b", "b", alpha, StateIdle)
		for index := range queuePerRecipient {
			key := "cc:s" + strings.Repeat("x", index)
			f.session(key, "s"+strings.Repeat("x", index), alpha, StateIdle)
			f.send(key, SendInput{To: "b", Message: "waiting"})
		}
		f.session("cc:late", "late", alpha, StateIdle)
		_, err := f.store.Send(f.ctx, "cc:late", Scope{}, SendInput{To: "b", Message: "fifty-first"})
		f.refused(err, ReasonRecipientFull)
		_, err = f.store.Send(f.ctx, "cc:late", Scope{}, SendInput{To: "#alpha", Message: "to the channel"})
		f.refused(err, ReasonRecipientFull) // a channel send never leaves a subscriber out silently
		// A response to a full requester is refused too, and so stays answerable later.
		f.session("cc:asker", "asker", alpha, StateIdle)
		f.clock.advance(time.Minute)
		asked := f.send("cc:asker", SendInput{To: "late", Message: "decide", Request: &RequestInput{Expects: "decision"}})
		for index := range queuePerRecipient {
			key := "cc:t" + strings.Repeat("y", index)
			f.session(key, "t"+strings.Repeat("y", index), beta, StateIdle)
			f.send(key, SendInput{To: "asker", Message: "waiting"})
		}
		_, err = f.store.Respond(f.ctx, "cc:late", RespondInput{RequestID: asked.RequestID, Decision: "approve"})
		f.refused(err, ReasonRecipientFull)
	})
	t.Run("loop", func(t *testing.T) {
		f := newFixture(t)
		f.session("cc:a", "a", alpha, StateIdle)
		f.session("cc:b", "b", alpha, StateIdle)
		for index := range exchangeLimit {
			f.clock.advance(3 * time.Second)
			from, to := "cc:a", "b"
			if index%2 == 1 {
				from, to = "cc:b", "a"
			}
			f.send(from, SendInput{To: to, Message: "round " + strings.Repeat("x", index)})
		}
		_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "round forty-one"})
		f.refused(err, ReasonLoop)
		// A person's prompt resets one side's count, which ends the pause for the pair.
		session := f.record("cc:b")
		session.Exchange = nil
		if err := writeRecord(f.store.place.session(hashKey("cc:b")), session, sessionBound); err != nil {
			t.Fatal(err)
		}
		f.send("cc:a", SendInput{To: "b", Message: "round forty-one"})
	})
}

func TestARetryWithTheSameKeyIsADuplicate(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	input := SendInput{To: "b", Message: "once", IdempotencyKey: "key-0001"}
	first := f.send("cc:a", input)
	again := f.send("cc:a", input)
	if again.Outcome != OutcomeDuplicate || again.MessageID != first.MessageID || len(again.Recipients) != 1 {
		t.Fatalf("%+v", again)
	}
	// A key whose message is gone is a leftover: the send is fresh.
	if err := os.Remove(f.store.place.commit(first.MessageID)); err != nil {
		t.Fatal(err)
	}
	f.clock.advance(time.Minute)
	fresh := f.send("cc:a", input)
	if fresh.Outcome != OutcomeAccepted || fresh.MessageID == first.MessageID {
		t.Fatalf("%+v", fresh)
	}
}

func TestSendInputsAreChecked(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	control := strings.Repeat("\u0001", 256)
	meta := map[string]string{}
	for index := range 16 {
		meta["k"+string(rune('a'+index))] = control
	}
	cases := map[string]struct {
		input  SendInput
		reason string
	}{
		"empty":           {SendInput{To: "b"}, ReasonInvalid},
		"too long":        {SendInput{To: "b", Message: strings.Repeat("x", MaxBodyBytes+1)}, ReasonTooLarge},
		"bad utf8":        {SendInput{To: "b", Message: "\xff"}, ReasonInvalid},
		"two-line sum":    {SendInput{To: "b", Message: "x", Summary: "a\nb"}, ReasonInvalid},
		"priority":        {SendInput{To: "b", Message: "x", Priority: "urgent"}, ReasonInvalid},
		"expiry low":      {SendInput{To: "b", Message: "x", ExpiresInSeconds: 59}, ReasonInvalid},
		"expiry high":     {SendInput{To: "b", Message: "x", ExpiresInSeconds: 604801}, ReasonInvalid},
		"options":         {SendInput{To: "b", Message: "x", Request: &RequestInput{Expects: "decision", Options: []string{"a", "b"}}}, ReasonInvalid},
		"one option":      {SendInput{To: "b", Message: "x", Request: &RequestInput{Expects: "answer", Options: []string{"a"}}}, ReasonInvalid},
		"same options":    {SendInput{To: "b", Message: "x", Request: &RequestInput{Expects: "answer", Options: []string{"a", "a"}}}, ReasonInvalid},
		"meta key":        {SendInput{To: "b", Message: "x", Meta: map[string]string{"a-b": "x"}}, ReasonInvalid},
		"short key":       {SendInput{To: "b", Message: "x", IdempotencyKey: "short"}, ReasonInvalid},
		"file no path":    {SendInput{To: "b", Message: "x", Attachments: []Attachment{{Kind: "file"}}}, ReasonInvalid},
		"file escapes":    {SendInput{To: "b", Message: "x", Attachments: []Attachment{{Kind: "file", Path: "a/../../b"}}}, ReasonInvalid},
		"commit no rev":   {SendInput{To: "b", Message: "x", Attachments: []Attachment{{Kind: "commit"}}}, ReasonInvalid},
		"artifact form":   {SendInput{To: "b", Message: "x", Attachments: []Attachment{{Kind: "artifact", URI: "jst:other/x"}}}, ReasonInvalid},
		"url http":        {SendInput{To: "b", Message: "x", Attachments: []Attachment{{Kind: "url", URI: "http://x"}}}, ReasonInvalid},
		"rendered 12 KiB": {SendInput{To: "b", Message: "x", Meta: meta}, ReasonTooLarge},
		"thread id":       {SendInput{To: "b", Message: "x", ThreadID: "msg_x"}, ReasonInvalid},
		"no thread":       {SendInput{To: "b", Message: "x", ThreadID: "msg_" + strings.Repeat("0", 26)}, ReasonUnknownMessage},
	}
	for name, test := range cases {
		_, err := f.store.Send(f.ctx, "cc:a", Scope{}, test.input)
		if ReasonOf(err) != test.reason {
			t.Errorf("%s: got %v, want %s", name, err, test.reason)
		}
	}
	good := f.send("cc:a", SendInput{To: "b", Message: "x", Attachments: []Attachment{
		{Kind: "file", Repository: "#web", Path: "internal/api/handler.go", Revision: "4f2a9c1", Lines: "40-72", Title: "the handler"},
		{Kind: "commit", Revision: "4f2a9c1"},
		{Kind: "artifact", URI: "jst:review/task/1"},
		{Kind: "url", URI: "https://example.com/x"},
	}})
	if good.Outcome != OutcomeAccepted {
		t.Fatalf("%+v", good)
	}
}

func TestARequestIsAnsweredOnce(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.session("cc:c", "c", alpha, StateIdle)
	asked := f.send("cc:a", SendInput{To: "b", Message: "run the tests?", Request: &RequestInput{Expects: "answer", Options: []string{"now", "later"}}})
	if !requestIDPattern.MatchString(asked.RequestID) || asked.RequestID[4:] == asked.MessageID[4:] ||
		f.commit(asked.MessageID).RequestState != "open" {
		t.Fatalf("%+v", asked)
	}
	_, err := f.store.Respond(f.ctx, "cc:c", RespondInput{RequestID: asked.RequestID, Decision: "answer", Choice: "now"})
	f.refused(err, ReasonNotRecipient)
	_, err = f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "approve"})
	f.refused(err, ReasonInvalid)
	_, err = f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "answer", Choice: "maybe"})
	f.refused(err, ReasonInvalid)
	answer, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "answer", Choice: "now", Reason: "free now"})
	if err != nil || answer.ThreadID != asked.ThreadID || answer.Recipients[0].Session != "a" {
		t.Fatalf("%+v %v", answer, err)
	}
	commit := f.commit(asked.MessageID)
	if commit.RequestState != "answered" || commit.ResponseID != answer.MessageID {
		t.Fatalf("%+v", commit)
	}
	if delivery := f.delivery(asked.MessageID, "cc:b"); delivery.State != "acted" {
		t.Fatalf("%+v", delivery)
	}
	if exists(filepath.Join(f.store.place.inbox(hashKey("cc:b")), asked.MessageID)) {
		t.Fatal("an acted delivery keeps no inbox marker")
	}
	var envelope Envelope
	if err := readRecord(f.store.place.envelope(answer.MessageID), envelopeBound, &envelope); err != nil ||
		envelope.Kind != "response" || envelope.ReplyTo != asked.MessageID || envelope.Summary != "answer: free now" {
		t.Fatalf("%+v %v", envelope, err)
	}
	_, err = f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "answer", Choice: "later"})
	f.refused(err, ReasonAlreadyAnswered)
	_, err = f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: "req_" + strings.Repeat("0", 26), Decision: "approve"})
	f.refused(err, ReasonUnknownMessage)
}

func TestAnAnswerPastTheDeadlineClosesTheRequestWithOneNotice(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}, ExpiresInSeconds: 60})
	f.clock.advance(time.Minute)
	_, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "approve"})
	f.refused(err, ReasonRequestClosed)
	commit := f.commit(asked.MessageID)
	if commit.RequestState != "expired" || commit.Notice != "owed" || commit.Closed == "" {
		t.Fatalf("%+v", commit)
	}
	delivery := f.delivery(asked.MessageID, "cc:b")
	if delivery.State != "expired" || delivery.Notice != "" {
		t.Fatalf("the notice is the request's, once: %+v", delivery)
	}
	_, err = f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "approve"})
	f.refused(err, ReasonRequestClosed)
}

func TestAClosedRequestWhoseDeliveryACrashLeftPendingIsRepaired(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}})
	commit := f.commit(asked.MessageID)
	commit.RequestState, commit.Notice = "expired", "owed"
	if err := writeRecord(f.store.place.commit(asked.MessageID), commit, commitBound); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "approve"})
	f.refused(err, ReasonRequestClosed)
	if delivery := f.delivery(asked.MessageID, "cc:b"); delivery.State != "expired" || delivery.Notice != "" {
		t.Fatalf("%+v", delivery)
	}
}

func TestAResponseToARequesterGoneIsDroppedWithItsNotice(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}})
	f.session("cc:a", "", alpha, StateGone)
	answer, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "reject", Reason: "no"})
	if err != nil {
		t.Fatal(err)
	}
	delivery := f.delivery(answer.MessageID, "cc:a")
	if delivery.State != "dropped" || delivery.Reason != "recipient_ended" || delivery.Notice != "owed" {
		t.Fatalf("%+v", delivery)
	}
	if exists(filepath.Join(f.store.place.inbox(hashKey("cc:a")), answer.MessageID)) {
		t.Fatal("a dropped delivery has no inbox marker")
	}
	if f.commit(asked.MessageID).RequestState != "answered" {
		t.Fatal("the request is answered all the same")
	}
}

func TestIdleSubscriptions(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:busy", "busy", alpha, StateBusy)
	f.session("cc:idle", "idle", alpha, StateIdle)
	if _, err := f.store.Register(f.ctx, Registration{Key: "tk:unknown", KeySource: SourceToken, Engine: EngineOther,
		Name: "unknown", Channel: alpha}); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "unknown", NotifyWhenIdle: true})
	f.refused(err, ReasonIdleUnobservable)

	subscribed := f.send("cc:a", SendInput{To: "busy", NotifyWhenIdle: true})
	if subscribed.Outcome != OutcomeSubscribed || subscribed.Fired || subscribed.Until != stamp(f.clock.at.Add(12*time.Hour)) {
		t.Fatalf("%+v", subscribed)
	}
	watchers := f.record("cc:busy").IdleWatchers
	if len(watchers) != 1 || !watchers[0].Armed || watchers[0].Key != "cc:a" {
		t.Fatalf("%+v", watchers)
	}
	f.clock.advance(time.Hour)
	renewed := f.send("cc:a", SendInput{To: "busy", NotifyWhenIdle: true})
	if renewed.Until != stamp(f.clock.at.Add(12*time.Hour)) || len(f.record("cc:busy").IdleWatchers) != 1 {
		t.Fatalf("a repeat renews until: %+v", renewed)
	}

	at := f.send("cc:a", SendInput{To: "idle", NotifyWhenIdle: true})
	if !at.Fired || len(f.record("cc:a").Notices) != 1 || f.record("cc:a").Notices[0].State != "owed" {
		t.Fatalf("an idle target is a notice at once: %+v %+v", at, f.record("cc:a").Notices)
	}
	again := f.send("cc:a", SendInput{To: "idle", NotifyWhenIdle: true})
	if !again.Fired || again.FiredAt != at.FiredAt || len(f.record("cc:a").Notices) != 1 {
		t.Fatalf("a repeat within 60 s is told it fired, without a second notice: %+v", again)
	}

	withMessage := f.send("cc:a", SendInput{To: "busy", Message: "tell me when done", NotifyWhenIdle: true})
	watchers = f.record("cc:busy").IdleWatchers
	if len(watchers) != 1 || watchers[0].Armed || watchers[0].MessageID != withMessage.MessageID {
		t.Fatalf("with a message, the subscription replaces the earlier one and waits for delivery: %+v", watchers)
	}
}

func TestRecoveryRollsAnUncommittedSendBack(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	id := "msg_" + strings.Repeat("1", 26)
	err := f.store.update(f.ctx, func(transaction *transaction) error {
		if err := transaction.publishJournal(journalEntry{Op: opSend, ID: id, Sender: "cc:a"}); err != nil {
			return err
		}
		envelope := Envelope{V: 1, ID: id, Recipients: []string{hashKey("cc:b")}}
		data, _ := encodeRecord(envelope, envelopeBound)
		delivery := Delivery{V: 1, ID: id, Key: "cc:b", Route: "hooks", State: "pending"}
		return transaction.writeBeforeCommit("half", data, envelope, []Delivery{delivery})
	})
	if err != nil {
		t.Fatal(err)
	}
	// The process died here: no commit record. The next operation, whatever it is, rolls it back.
	if _, err := f.store.Sessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{f.store.place.body(id), f.store.place.envelope(id), f.store.place.deliveries(id),
		filepath.Join(f.store.place.journal(), id)} {
		if exists(path) {
			t.Errorf("%s left after the rollback", path)
		}
	}
}

func TestRecoveryRollsACommittedSendForward(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	id := "msg_" + strings.Repeat("2", 26)
	key := filepath.Base(f.store.place.idempotencyEntry("cc:a", "retry-key"))
	err := f.store.update(f.ctx, func(transaction *transaction) error {
		entry := journalEntry{Op: opSend, ID: id, Sender: "cc:a", Idempotency: key}
		if err := transaction.publishJournal(entry); err != nil {
			return err
		}
		envelope := Envelope{V: 1, ID: id, ThreadID: id, Kind: "message", Recipients: []string{hashKey("cc:b")}}
		data, _ := encodeRecord(envelope, envelopeBound)
		delivery := Delivery{V: 1, ID: id, Key: "cc:b", Route: "hooks", State: "pending"}
		if err := transaction.writeBeforeCommit("whole", data, envelope, []Delivery{delivery}); err != nil {
			return err
		}
		return transaction.writeCommit(Commit{V: 1, ID: id, Committed: stamp(transaction.now)})
	})
	if err != nil {
		t.Fatal(err)
	}
	// The process died after the commit record: the next operation finishes the send.
	retry, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "whole", IdempotencyKey: "retry-key"})
	if err != nil || retry.Outcome != OutcomeDuplicate || retry.MessageID != id {
		t.Fatalf("%+v %v", retry, err)
	}
	for _, path := range []string{
		filepath.Join(f.store.place.inbox(hashKey("cc:b")), id),
		filepath.Join(f.store.place.outbox(hashKey("cc:a")), id),
	} {
		if !exists(path) {
			t.Errorf("%s missing after the roll forward", path)
		}
	}
	if exists(filepath.Join(f.store.place.journal(), id)) {
		t.Error("the journal entry is removed once rolled forward")
	}
}

func TestACommitThatCannotBeMadeDurableStopsTheStore(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	unsynced := errors.New("the disk refused")
	f.store.writeCommitFile = func(path string, data []byte, mode os.FileMode, limit int) error {
		if err := durable.WriteAtomic(path, data, mode, limit); err != nil {
			return err
		}
		return errors.Join(durable.ErrNotSynced, unsynced)
	}
	f.store.syncFolder = func(string) error { return unsynced }
	result := f.send("cc:a", SendInput{To: "b", Message: "maybe", IdempotencyKey: "key-maybe"})
	if result.Outcome != OutcomeUncertain {
		t.Fatalf("%+v", result)
	}
	_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "maybe", IdempotencyKey: "key-maybe"})
	f.refused(err, ReasonStoreBusy)
	if _, err := f.store.Sessions(f.ctx); ReasonOf(err) != ReasonStoreBusy {
		t.Fatalf("a reader is stopped too: %v", err)
	}
	if exists(filepath.Join(f.store.place.inbox(hashKey("cc:b")), result.MessageID)) {
		t.Fatal("nothing that follows from the commit is applied")
	}
	// The disk recovers: the next operation rolls the send forward, and the retry is a duplicate.
	f.store.syncFolder = durable.SyncFolder
	retry := f.send("cc:a", SendInput{To: "b", Message: "maybe", IdempotencyKey: "key-maybe"})
	if retry.Outcome != OutcomeDuplicate || retry.MessageID != result.MessageID {
		t.Fatalf("%+v", retry)
	}
}

func TestABrokenJournalEntryIsMovedAside(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	if err := durable.MakeFolder(f.store.place.journal(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.store.place.journal(), "garbage"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Sessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	if !exists(filepath.Join(f.store.place.broken(), "garbage")) {
		t.Fatal("the entry is in journal/broken")
	}
}

func TestAFullStoreRefusesASend(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	if err := writeRecord(f.store.place.usage(), usage{V: 1, Bytes: 63 << 20}, usageBound); err != nil {
		t.Fatal(err)
	}
	_, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "no room"})
	f.refused(err, ReasonCapacity)
	if err := os.Remove(f.store.place.usage()); err != nil {
		t.Fatal(err)
	}
	f.send("cc:a", SendInput{To: "b", Message: "room again: usage is rebuilt from the folders"})
}

func TestUsageIsAFunctionOfTheFiles(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	f.send("cc:a", SendInput{To: "b", Message: "count me", IdempotencyKey: "count-key"})
	var counted usage
	if err := readRecord(f.store.place.usage(), usageBound, &counted); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(f.store.place.usage()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Sessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	var rebuilt usage
	if err := readRecord(f.store.place.usage(), usageBound, &rebuilt); err != nil {
		t.Fatal(err)
	}
	if rebuilt != counted || rebuilt.Messages != 1 {
		t.Fatalf("counted %+v, rebuilt %+v", counted, rebuilt)
	}
}

func TestCheckRelatedNamesEachProblem(t *testing.T) {
	channels := []Channel{alpha, beta, {ID: strings.Repeat("d", 64), Name: "beta"}}
	problems := CheckRelated([]string{"#alpha", "#beta", "#delta", "web", "#common"}, channels)
	if len(problems) != 3 || !strings.Contains(problems[0], "several") || !strings.Contains(problems[1], "no channel") ||
		!strings.Contains(problems[2], "not a channel") {
		t.Fatalf("%v", problems)
	}
}

func TestASessionNamedMainIsAlwaysListedWithItsRef(t *testing.T) {
	f := newFixture(t)
	f.session("cc:parent", "parent", alpha, StateIdle)
	main := f.session("cc:main", "main", alpha, StateIdle)
	if _, err := f.store.Register(f.ctx, Registration{Key: "cc:child", KeySource: SourceEngine, Engine: EngineClaudeCode,
		Name: "child", Channel: alpha, Parent: "cc:parent", State: StateIdle}); err != nil {
		t.Fatal(err)
	}
	result := f.send("cc:child", SendInput{To: "[" + main.Ref + "]", Message: "to the session named main"})
	if result.Recipients[0].Session != "main ["+main.Ref+"]" {
		t.Fatalf("a bare `main` would address the parent: %+v", result)
	}
	if parent := f.send("cc:child", SendInput{To: "main", Message: "to my parent"}); parent.Recipients[0].Session != "parent" {
		t.Fatalf("%+v", parent)
	}
}

func TestARequestIDCannotBeDerivedFromItsMessageID(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	asked := f.send("cc:a", SendInput{To: "b", Message: "decide", Request: &RequestInput{Expects: "decision"}})
	_, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: "req_" + asked.MessageID[4:], Decision: "approve"})
	f.refused(err, ReasonUnknownMessage)
}

func TestAnswersToDifferentRequestsAreNotRepeats(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	first := f.send("cc:a", SendInput{To: "b", Message: "first?", Request: &RequestInput{Expects: "decision"}})
	second := f.send("cc:a", SendInput{To: "b", Message: "second?", Request: &RequestInput{Expects: "decision"}})
	for _, asked := range []SendResult{first, second} {
		if _, err := f.store.Respond(f.ctx, "cc:b", RespondInput{RequestID: asked.RequestID, Decision: "approve"}); err != nil {
			t.Fatalf("an answer without a message to %s: %v", asked.RequestID, err)
		}
	}
}

func TestAWatcherPastTheBoundIsRefusedBeforeTheSend(t *testing.T) {
	f := newFixture(t)
	f.session("cc:busy", "busy", alpha, StateBusy)
	for index := range maxWatchers {
		key := "cc:w" + strings.Repeat("x", index)
		f.session(key, "w"+strings.Repeat("x", index), alpha, StateIdle)
		f.send(key, SendInput{To: "busy", NotifyWhenIdle: true})
	}
	f.session("cc:late", "late", alpha, StateIdle)
	_, err := f.store.Send(f.ctx, "cc:late", Scope{}, SendInput{To: "busy", Message: "and me", NotifyWhenIdle: true})
	f.refused(err, ReasonCapacity)
	if len(f.record("cc:busy").IdleWatchers) != maxWatchers {
		t.Fatal("no other watcher is evicted")
	}
	// An existing watcher may replace its own subscription.
	f.send("cc:w", SendInput{To: "busy", Message: "replace mine", NotifyWhenIdle: true})
}

func TestReplayRewritesAnIdempotencyEntryACrashLeftEmpty(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	id := "msg_" + strings.Repeat("3", 26)
	name := filepath.Base(f.store.place.idempotencyEntry("cc:a", "empty-key"))
	err := f.store.update(f.ctx, func(transaction *transaction) error {
		entry := journalEntry{Op: opSend, ID: id, Sender: "cc:a", Idempotency: name}
		if err := transaction.publishJournal(entry); err != nil {
			return err
		}
		envelope := Envelope{V: 1, ID: id, ThreadID: id, Kind: "message", Recipients: []string{hashKey("cc:b")}}
		data, _ := encodeRecord(envelope, envelopeBound)
		delivery := Delivery{V: 1, ID: id, Key: "cc:b", Route: "hooks", State: "pending"}
		if err := transaction.writeBeforeCommit("whole", data, envelope, []Delivery{delivery}); err != nil {
			return err
		}
		if err := transaction.writeCommit(Commit{V: 1, ID: id, Committed: stamp(transaction.now)}); err != nil {
			return err
		}
		// The process died while it wrote the entry: the name exists, its content does not.
		return createExclusive(filepath.Join(transaction.place.idempotency(), name), nil)
	})
	if err != nil {
		t.Fatal(err)
	}
	retry, err := f.store.Send(f.ctx, "cc:a", Scope{}, SendInput{To: "b", Message: "whole", IdempotencyKey: "empty-key"})
	if err != nil || retry.Outcome != OutcomeDuplicate || retry.MessageID != id {
		t.Fatalf("%+v %v", retry, err)
	}
}

func TestACommitThatCannotBeLookedUpIsNeverRolledBack(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	id := "msg_" + strings.Repeat("4", 26)
	err := f.store.update(f.ctx, func(transaction *transaction) error {
		if err := transaction.publishJournal(journalEntry{Op: opSend, ID: id, Sender: "cc:a"}); err != nil {
			return err
		}
		envelope := Envelope{V: 1, ID: id, ThreadID: id, Kind: "message", Recipients: []string{hashKey("cc:b")}}
		data, _ := encodeRecord(envelope, envelopeBound)
		delivery := Delivery{V: 1, ID: id, Key: "cc:b", Route: "hooks", State: "pending"}
		if err := transaction.writeBeforeCommit("whole", data, envelope, []Delivery{delivery}); err != nil {
			return err
		}
		return transaction.writeCommit(Commit{V: 1, ID: id, Committed: stamp(transaction.now)})
	})
	if err != nil {
		t.Fatal(err)
	}
	// The commits folder cannot be searched for a moment: the lookup fails without saying the record
	// is absent, so nothing is rolled back.
	if err := os.Chmod(f.store.place.commits(), 0o000); err != nil {
		t.Fatal(err)
	}
	_, lookupErr := f.store.Sessions(f.ctx)
	if err := os.Chmod(f.store.place.commits(), 0o700); err != nil {
		t.Fatal(err)
	}
	if lookupErr == nil {
		t.Skip("this account can search a folder without permission (root)")
	}
	if !exists(f.store.place.envelope(id)) || !exists(f.store.place.body(id)) {
		t.Fatal("a commit that could not be looked up had its message removed")
	}
	if _, err := f.store.Sessions(f.ctx); err != nil || !exists(filepath.Join(f.store.place.inbox(hashKey("cc:b")), id)) {
		t.Fatalf("the next recovery rolls it forward: %v", err)
	}
}

func TestARollbackKeepsItsEntryUntilItsRemovalsAreDurable(t *testing.T) {
	f := newFixture(t)
	f.session("cc:a", "a", alpha, StateIdle)
	f.session("cc:b", "b", alpha, StateIdle)
	id := "msg_" + strings.Repeat("5", 26)
	err := f.store.update(f.ctx, func(transaction *transaction) error {
		if err := transaction.publishJournal(journalEntry{Op: opSend, ID: id, Sender: "cc:a"}); err != nil {
			return err
		}
		envelope := Envelope{V: 1, ID: id, Recipients: []string{hashKey("cc:b")}}
		data, _ := encodeRecord(envelope, envelopeBound)
		delivery := Delivery{V: 1, ID: id, Key: "cc:b", Route: "hooks", State: "pending"}
		return transaction.writeBeforeCommit("half", data, envelope, []Delivery{delivery})
	})
	if err != nil {
		t.Fatal(err)
	}
	deliveries := filepath.Dir(f.store.place.deliveries(id))
	if err := os.Remove(f.store.place.usage()); err != nil {
		t.Fatal(err)
	}
	f.store.syncFolder = func(folder string) error {
		if folder == deliveries {
			return errors.New("the disk refused")
		}
		return durable.SyncFolder(folder)
	}
	if _, err := f.store.Sessions(f.ctx); err == nil {
		t.Fatal("a rollback whose removals are not durable fails the operation")
	}
	if !exists(filepath.Join(f.store.place.journal(), id)) {
		t.Fatal("the entry stays until the removals are durable")
	}
	if exists(f.store.place.usage()) {
		t.Fatal("no usage is saved over removals that are not durable")
	}
	f.store.syncFolder = durable.SyncFolder
	if _, err := f.store.Sessions(f.ctx); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(f.store.place.journal(), id)) || exists(f.store.place.envelope(id)) {
		t.Fatal("the next recovery finishes the rollback")
	}
	var rebuilt usage
	if err := readRecord(f.store.place.usage(), usageBound, &rebuilt); err != nil || rebuilt.Messages != 0 ||
		rebuilt.Bytes != 2*deliverystate.SessionCharge+deliverystate.ChannelCharge {
		t.Fatalf("usage is rebuilt from what is left: %+v %v", rebuilt, err)
	}
}
