// Package docs holds the check of jackstay's own documents (its test): every relative link in a
// Markdown file leads to a file that is there, docs/decisions/README.md lists every decision, every
// module's licence is listed, and every file is in English (save test data that needs other scripts,
// and the notes a repository lists in .not-shipped, a file the public tree does not carry); and the
// rules of docs/STYLE.md a program can check (style_test.go).
// It is the docs check `just gate` runs when a change touches documents only (W-040).
package docs
