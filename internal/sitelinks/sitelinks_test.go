package sitelinks

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const base = "https://docs.test/jackstay/"

// builtSite writes the files of a built site: path -> contents.
func builtSite(t *testing.T, files map[string]string) string {
	t.Helper()
	folder := t.TempDir()
	for name, contents := range files {
		file := filepath.Join(folder, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return folder
}

func TestLinksThatLeadSomewherePass(t *testing.T) {
	folder := builtSite(t, map[string]string{
		"index.html": `<a href="guide/">relative to the home page</a>
			<a href="/jackstay/guide/install/">root-relative</a>
			<a href="https://docs.test/jackstay/design/">absolute</a>
			<a href="guide/install/#the-quick-install">with a fragment</a>
			<script src="/jackstay/js/main.min.js"></script>
			<a href="install.sh">a file</a>
			<a href="#top">this page</a> <h1 id="top">jackstay</h1>
			<a href="#">the theme's no-op link</a>
			<a href="https://github.com/zima-oss/jackstay">another site</a>
			<a href="mailto:someone@docs.test">mail</a>
			<a href="?q=a&amp;b=c">a query on this page</a>`,
		"guide/index.html":         `<a href="install/">down</a> <a href="../design/">across</a> <a href="..">up</a>`,
		"guide/install/index.html": `<h2 id="the-quick-install">The quick install</h2> <a href="../../">home</a>`,
		"design/index.html":        `<a href="/jackstay/">home</a>`,
		"js/main.min.js":           "",
		"install.sh":               "",
	})
	problems, err := Check(folder, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 0 {
		t.Fatalf("problems: %v", problems)
	}
}

func TestAMinifiedPageIsReadToo(t *testing.T) {
	folder := builtSite(t, map[string]string{
		"index.html":       `<a href=guide/ class=x>guide</a><a href='guide/#quick'>q</a><a href=gone/>gone</a><a href=guide/#renamed>r</a>`,
		"guide/index.html": `<h2 id=quick>Quick</h2>`,
	})
	problems, err := Check(folder, base)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, problem := range problems {
		lines = append(lines, problem.String())
	}
	want := `index.html: "gone/" leads to no file of the site (gone/)` + "\n" +
		`index.html: "guide/#renamed" names no heading of guide/index.html (#renamed)`
	if got := strings.Join(lines, "\n"); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestOnlyTagsCountNeverTextThatLooksLikeThem(t *testing.T) {
	folder := builtSite(t, map[string]string{
		"index.html": `<a href=#old-title>Jump</a>
			<p>Use id=old-title in the example, or href=missing/ and <code>&lt;a href="gone/"&gt;</code>.</p>
			<!-- <a href="commented-out/">old</a> <h2 id="in-a-comment"> -->
			<script>const link = '<a href="from-a-script/">'; document.write('<h2 id="written">')</script>
			<a href="#in-a-comment">c</a> <a href="#written">s</a>`,
	})
	problems, err := Check(folder, base)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, problem := range problems {
		lines = append(lines, problem.String())
	}
	want := `index.html: "#in-a-comment" names no heading of index.html (#in-a-comment)` + "\n" +
		`index.html: "#old-title" names no heading of index.html (#old-title)` + "\n" +
		`index.html: "#written" names no heading of index.html (#written)`
	if got := strings.Join(lines, "\n"); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestLinksAfterForeignContentAreRead(t *testing.T) {
	// A browser closes an SVG <style/> at its "/>"; a page tokenized without the tree would read the
	// rest of it as the style's text.
	folder := builtSite(t, map[string]string{
		"index.html": `<svg><style/></svg><a href="missing/">Broken</a><math><mi id="in-math">x</mi></math><a href="#in-math">m</a>`,
	})
	problems, err := Check(folder, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(problems) != 1 || problems[0].String() != `index.html: "missing/" leads to no file of the site (missing/)` {
		t.Fatalf("problems: %v", problems)
	}
}

func TestEachBrokenLinkIsNamed(t *testing.T) {
	folder := builtSite(t, map[string]string{
		"index.html": `<a href="guide/gone/">a renamed page</a>
			<a href="/guide/">outside the base path</a>
			<a href="https://docs.test/other/">another path on the same host</a>
			<img src="images/logo.svg">`,
		"guide/index.html": `<a href="../desing/">a typo</a> <a href="#gone">a renamed heading</a>
			<a href="/jackstay/design/#old-title">a heading renamed on another page</a>`,
		"design/index.html": `<h2 id="new-title">New title</h2>`,
	})
	problems, err := Check(folder, base)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, problem := range problems {
		lines = append(lines, problem.String())
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{
		`guide/index.html: "../desing/" leads to no file of the site (desing/)`,
		`index.html: "guide/gone/" leads to no file of the site (guide/gone/)`,
		`index.html: "/guide/" leaves the site's base path /jackstay/`,
		`index.html: "https://docs.test/other/" leaves the site's base path /jackstay/`,
		`index.html: "images/logo.svg" leads to no file of the site (images/logo.svg)`,
		`guide/index.html: "#gone" names no heading of guide/index.html (#gone)`,
		`guide/index.html: "/jackstay/design/#old-title" names no heading of design/index.html (#old-title)`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in:\n%s", want, got)
		}
	}
	if len(problems) != 7 {
		t.Errorf("%d problems:\n%s", len(problems), got)
	}
}

func TestMainSaysWhatItFound(t *testing.T) {
	good := builtSite(t, map[string]string{"index.html": `<a href="./">home</a>`})
	broken := builtSite(t, map[string]string{"index.html": `<a href="gone/">gone</a>`})
	for _, check := range []struct {
		args   []string
		status int
		output string
	}{
		{[]string{good, base}, 0, "every link of the site to itself leads somewhere"},
		{[]string{broken, base}, 1, `"gone/" leads to no file`},
		{[]string{good}, 2, "usage:"},
		{[]string{t.TempDir(), base}, 2, "holds no built site"},
		{[]string{good, "/jackstay/"}, 2, "not an absolute base address"},
	} {
		var stdout, stderr bytes.Buffer
		status := Main(check.args, &stdout, &stderr)
		if status != check.status || !strings.Contains(stdout.String()+stderr.String(), check.output) {
			t.Errorf("%v: exit %d, %s%s", check.args, status, stdout.String(), stderr.String())
		}
	}
}
