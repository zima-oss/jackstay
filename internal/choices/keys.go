package choices

import (
	"strings"

	"github.com/zima-oss/jackstay/internal/adopt/jsondoc"
)

// reviewUse is `review.use`: the reviewers `jst review` runs without --reviewer.
type reviewUse struct{}

func (reviewUse) Name() string { return "review.use" }

func (reviewUse) Read(document *jsondoc.Object) (string, bool) {
	review := document.Object("review")
	if review == nil {
		return "", false
	}
	raw, present := review.Get("use")
	list, isList := raw.([]any)
	if !present || !isList {
		return "", false
	}
	// An empty list is a choice too: every run names its reviewer.
	names := make([]string, 0, len(list))
	for _, item := range list {
		name, _ := item.(string)
		names = append(names, name)
	}
	return strings.Join(names, ","), true
}

func (reviewUse) Write(document *jsondoc.Object, value string) {
	review := document.Object("review")
	if review == nil {
		review = jsondoc.NewObject()
	}
	names := []any{}
	for _, name := range strings.Split(value, ",") {
		names = append(names, name)
	}
	review.Set("use", names)
	document.Set("review", review)
}

// chatEnabled is `chat.enabled`, written either way so the answer is kept. The chat section needs
// schema 3 and a pin of 0.6.0 or later; adopt does not ask it of an older pin. Off also turns off
// the engine integrations, which need chat on.
type chatEnabled struct{}

func (chatEnabled) Name() string { return "chat.enabled" }

func (chatEnabled) Read(document *jsondoc.Object) (string, bool) {
	chat := document.Object("chat")
	if chat == nil {
		return "", false
	}
	raw, present := chat.Get("enabled")
	if !present {
		return "", false
	}
	if raw == true {
		return "on", true
	}
	return "off", true
}

func (chatEnabled) Write(document *jsondoc.Object, value string) {
	chat := document.Object("chat")
	if chat == nil {
		chat = jsondoc.NewObject()
	}
	chat.Set("enabled", value == "on")
	if value != "on" {
		for _, integration := range []string{"codex_bridge", "claude_code"} {
			if engine := chat.Object(integration); engine != nil {
				if enabled, _ := engine.Get("enabled"); enabled == true {
					engine.Set("enabled", false)
				}
			}
		}
	}
	document.Set("chat", chat)
}
