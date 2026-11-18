// Package sitelinks checks a built documentation site (W-076): every link of a page to the site
// itself leads to a file the build wrote, and to a heading (an element's id) that page has when it
// names one, and none leaves the site's base path. The documents'
// own links are checked as the pages are made (site/layouts/_markup/render-link.html); this reads
// what the theme and the site's own pages write besides (menus, the sidebar, the home page's
// buttons and cards), relative links included, as a browser resolves them. A page is read as a
// browser reads it, parsed into elements by golang.org/x/net/html (SVG and MathML included): only
// elements' attributes count, never text, comments or scripts that look like them.
package sitelinks

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/net/html"
)

// tagsOf parses a page as a browser does and returns the links its elements carry (href and src)
// and the ids they give, which a link's #fragment names.
func tagsOf(contents []byte) (links []string, ids map[string]bool, err error) {
	document, err := html.Parse(bytes.NewReader(contents))
	if err != nil {
		return nil, nil, err
	}
	ids = map[string]bool{}
	for node := range document.Descendants() {
		if node.Type != html.ElementNode {
			continue
		}
		for _, attribute := range node.Attr {
			switch attribute.Key {
			case "href", "src":
				links = append(links, attribute.Val)
			case "id":
				ids[attribute.Val] = true
			}
		}
	}
	return links, ids, nil
}

// site is a built site being checked: its folder, its address, and the ids of each page read.
type site struct {
	folder string
	base   *url.URL
	ids    map[string]map[string]bool // page file, relative to folder -> its ids
}

// Problem is one link that leads nowhere.
type Problem struct {
	Page   string // the page that has it, relative to the site's folder
	Link   string // as the page writes it
	Reason string
}

func (problem Problem) String() string {
	return fmt.Sprintf("%s: %q %s", problem.Page, problem.Link, problem.Reason)
}

// Check reads every .html file under folder, a site built for the address base (its baseURL, such
// as https://zima-oss.github.io/jackstay/), and returns each link to that host that leaves the
// base path or names no file the build wrote. Links to other hosts and other schemes are not
// followed.
func Check(folder, base string) ([]Problem, error) {
	baseURL, err := url.Parse(base)
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return nil, fmt.Errorf("not an absolute base address: %q", base)
	}
	if !strings.HasSuffix(baseURL.Path, "/") {
		baseURL.Path += "/"
	}
	checked := &site{folder: folder, base: baseURL, ids: map[string]map[string]bool{}}
	var problems []Problem
	walkError := filepath.WalkDir(folder, func(file string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(file, ".html") {
			return nil
		}
		relative, err := filepath.Rel(folder, file)
		if err != nil {
			return err
		}
		page := filepath.ToSlash(relative)
		contents, err := os.ReadFile(file) // #nosec G304 G122 -- a page of the site being checked
		if err != nil {
			return err
		}
		links, ids, err := tagsOf(contents)
		if err != nil {
			return fmt.Errorf("%s: %w", page, err)
		}
		checked.ids[page] = ids
		problems = append(problems, checked.checkPage(page, links)...)
		return nil
	})
	if walkError != nil {
		return nil, walkError
	}
	slices.SortFunc(problems, func(left, right Problem) int {
		return strings.Compare(left.String(), right.String())
	})
	return slices.Compact(problems), nil
}

func (checked *site) checkPage(page string, links []string) []Problem {
	// The page's own address: a/b/index.html is served as a/b/, which relative links start from.
	pageURL := *checked.base
	pageURL.Path = checked.base.Path + strings.TrimSuffix(page, "index.html")
	var problems []Problem
	for _, link := range links {
		if reason := checked.checkLink(&pageURL, link); reason != "" {
			problems = append(problems, Problem{Page: page, Link: link, Reason: reason})
		}
	}
	return problems
}

// checkLink says why link, on the page at pageURL, leads nowhere; "" when it leads to a file of the
// site (and to an id of that page when it names one), to another host, or is no link to follow.
func (checked *site) checkLink(pageURL *url.URL, link string) string {
	if link == "" || link == "#" {
		return ""
	}
	reference, err := url.Parse(link)
	if err != nil {
		return "is no address"
	}
	target := pageURL.ResolveReference(reference)
	if target.Scheme != checked.base.Scheme || target.Host != checked.base.Host {
		return "" // another site, or mailto: and the like
	}
	if !strings.HasPrefix(target.Path, checked.base.Path) {
		return "leaves the site's base path " + checked.base.Path
	}
	inside := strings.TrimPrefix(target.Path, checked.base.Path)
	file := ""
	for _, candidate := range []string{inside, path.Join(inside, "index.html")} {
		if candidate != "" && candidate != "." && checked.exists(candidate) {
			file = candidate
			break
		}
	}
	if inside == "" {
		file = "index.html"
	}
	if file == "" {
		return "leads to no file of the site (" + inside + ")"
	}
	if target.Fragment == "" || !strings.HasSuffix(file, ".html") {
		return ""
	}
	ids, err := checked.idsOf(file)
	if err != nil {
		return "leads to a page that cannot be read: " + err.Error()
	}
	if !ids[target.Fragment] {
		return "names no heading of " + file + " (#" + target.Fragment + ")"
	}
	return ""
}

func (checked *site) exists(inside string) bool {
	info, err := os.Stat(filepath.Join(checked.folder, filepath.FromSlash(inside))) // #nosec G703 -- a resolved path under the base path, dot segments removed: a file of the site, only looked at
	return err == nil && !info.IsDir()
}

// idsOf is the set of ids of a page of the site, read once.
func (checked *site) idsOf(file string) (map[string]bool, error) {
	if ids, ok := checked.ids[file]; ok {
		return ids, nil
	}
	contents, err := os.ReadFile(filepath.Join(checked.folder, filepath.FromSlash(file))) // #nosec G304 G703 -- a page of the site being checked
	if err != nil {
		return nil, err
	}
	_, ids, err := tagsOf(contents)
	if err != nil {
		return nil, err
	}
	checked.ids[file] = ids
	return ids, nil
}

// Main is `go run ./cmd/jst-sitelinks FOLDER BASE`: exit 0 every link leads somewhere; 1 one does
// not (each is named); 2 a bad request.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: jst-sitelinks FOLDER BASE (a site built with --baseURL BASE into FOLDER)")
		return 2
	}
	if _, err := os.Stat(filepath.Join(args[0], "index.html")); err != nil {
		fmt.Fprintf(stderr, "jst-sitelinks: %s holds no built site\n", args[0])
		return 2
	}
	problems, err := Check(args[0], args[1])
	if err != nil {
		fmt.Fprintln(stderr, "jst-sitelinks:", err)
		return 2
	}
	for _, problem := range problems {
		fmt.Fprintln(stderr, "jst-sitelinks:", problem)
	}
	if len(problems) > 0 {
		return 1
	}
	fmt.Fprintln(stdout, "jst-sitelinks: every link of the site to itself leads somewhere")
	return 0
}
