package templates

import (
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedTemplatesArePinnedAndUseOnlyJackstaysValues(t *testing.T) {
	for _, template := range Embedded() {
		if problems := PinProblems(template.Text); len(problems) > 0 {
			t.Errorf("%s: %v", template.Name, problems)
		}
		if !strings.Contains(template.Text, "uses:") {
			t.Errorf("%s has no action: the pin check checked nothing", template.Name)
		}
		for _, name := range Placeholders(template.Text) {
			if !slices.Contains(ValueNames, name) {
				t.Errorf("%s uses @@%s@@, which jackstay does not define", template.Name, name)
			}
		}
		for _, word := range []string{"self-hosted", "JACKSTAY_APP_CLIENT_ID", "container:"} {
			if strings.Contains(template.Text, word) {
				t.Errorf("%s names %q: an organisation's detail belongs in a profile (W-059)", template.Name, word)
			}
		}
	}
}

func TestDownloadTemplatesVerifyConfiguredProvenanceBeforeUnpacking(t *testing.T) {
	for _, template := range Embedded() {
		if template.Name != "card-line" && template.Name != "release" {
			continue
		}
		text := template.Text
		for _, fragment := range []string{
			"JACKSTAY_PROVENANCE: ${{ vars.JACKSTAY_PROVENANCE }}",
			"JACKSTAY_LINUX_AMD64_SHA256: ${{ vars.JACKSTAY_LINUX_AMD64_SHA256 }}",
			"gh attestation verify \"$archive\" --repo @@jackstay_repository@@",
			"--signer-workflow zima-oss/jackstay/.github/workflows/release.yml",
			"--source-ref \"refs/tags/$JACKSTAY_VERSION\"",
		} {
			if !strings.Contains(text, fragment) {
				t.Errorf("%s lacks %q", template.Name, fragment)
			}
		}
		if strings.Index(text, "gh attestation verify") > strings.Index(text, "tar -xzf") {
			t.Errorf("%s unpacks before verifying provenance", template.Name)
		}
	}
}

func TestPinProblemsFindsEveryUnpinnedUse(t *testing.T) {
	text := strings.Join([]string{
		"      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
		"      - uses: actions/checkout@v4",
		"        uses: actions/setup-go@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"      - uses: ./local/action",
		"      - uses: docker://alpine@sha256:" + strings.Repeat("a", 64),
		"      - uses: docker://alpine:3",
	}, "\n")
	problems := PinProblems(text)
	if len(problems) != 3 {
		t.Fatalf("want 3 problems (a tag, a hash with no version comment, an image by tag), got %v", problems)
	}
	for index, line := range []string{"line 2", "line 3", "line 6"} {
		if !strings.HasPrefix(problems[index], line) {
			t.Errorf("problem %d: %s", index, problems[index])
		}
	}
}

func TestRenderNeedsEveryValue(t *testing.T) {
	got, err := Render("a @@one@@ b @@two@@ ${{ github.token }}", map[string]string{"one": "1", "two": "2"})
	if err != nil || got != "a 1 b 2 ${{ github.token }}" {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := Render("@@missing@@", nil); err == nil {
		t.Fatal("a value with no definition was rendered")
	}
}

func TestTheHeaderSaysWhoWroteTheFileAndWhetherItWasEdited(t *testing.T) {
	text := Marked("card-line", "name: x\non: push\n")
	marked, name, intact := Header(text)
	if !marked || name != "card-line" || !intact {
		t.Fatalf("got %v %q %v", marked, name, intact)
	}
	if _, _, intact := Header(text + "# one more line\n"); intact {
		t.Fatal("an edited body still matched its hash")
	}
	if marked, _, _ := Header("name: x\n"); marked {
		t.Fatal("a project's own file was taken for jackstay's")
	}
	if marked, name, intact := Header("# jackstay-adopt: broken\nname: x\n"); !marked || name != "" || intact {
		t.Fatal("a header that cannot be read must count as edited")
	}
}
