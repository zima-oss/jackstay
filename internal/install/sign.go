package install

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/zima-oss/jackstay/internal/homedir"
	"github.com/zima-oss/jackstay/internal/layout"
	"github.com/zima-oss/jackstay/internal/prove"
	"github.com/zima-oss/jackstay/internal/supervise"
)

// Signing at install (W-070; docs/design/implementation.md section 7.4): on a Mac whose jackstay home
// names a code-signing identity, the binary of a new version folder is signed with it and a stable
// identifier before the folder becomes live, so a firewall's rule (LuLu) follows jst across
// installs. Without the setting, and never off macOS, the binary keeps the Go linker's ad-hoc
// signature, as before.

// SigningIdentifier is the identifier every signed jst carries: the binary's name, as the release
// archives' signature (GoReleaser's quill) carries it too, so one identifier names jst everywhere
// (W-075). An install signed with an earlier identifier is not signed by the identity, and the next
// install of its version signs it again (Resign).
const SigningIdentifier = "jst"

// IdentityFile is the setting's file in jackstay's shared folder: one line, the identity's name
// exactly as `security find-identity -v -p codesigning` prints it (between the quotes). A file of this machine's
// jackstay home, not an environment variable: every process reads it alike, the agent sandbox and a
// hook included, and JACKSTAY_HOME keeps a test's apart.
const IdentityFile = "codesign-identity"

// signTimeout bounds each codesign and security call.
const signTimeout = 60 * time.Second

// SigningIdentity is the configured identity ("" when there is none, or off macOS) and the
// setting's path.
func SigningIdentity() (identity, path string, err error) {
	path, err = homedir.SharedPath(IdentityFile)
	if err != nil {
		return "", "", err
	}
	if runtime.GOOS != "darwin" {
		return "", path, nil
	}
	identity, err = ReadIdentitySetting(path)
	return identity, path, err
}

// ReadIdentitySetting reads the setting's file at path: its one line, "" when there is no file.
func ReadIdentitySetting(path string) (string, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- jackstay's own setting
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	identity := strings.TrimSpace(string(data))
	if strings.Contains(identity, "\n") {
		return "", fmt.Errorf("%s holds more than one line: one identity is expected", path)
	}
	return identity, nil
}

// SignArgs is the codesign call that signs file with identity.
func SignArgs(identity, file string) []string {
	return []string{"codesign", "--sign", identity, "--identifier", SigningIdentifier, "--options", "runtime", "--timestamp", "--force", file}
}

// VerifyArgs is the codesign call that verifies file's signature strictly.
func VerifyArgs(file string) []string {
	return []string{"codesign", "--verify", "--strict", file}
}

// DisplayArgs is the codesign call that describes file's signature (on standard error).
func DisplayArgs(file string) []string {
	return []string{"codesign", "--display", "--verbose=2", file}
}

// Signature is what `codesign --display --verbose=2` says of a binary.
type Signature struct {
	Identifier string
	// Authority is the first authority (the signing certificate), "" for an ad-hoc signature.
	Authority string
	AdHoc     bool
}

// ReadSignature reads codesign's description.
func ReadSignature(text string) Signature {
	var found Signature
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "Identifier":
			found.Identifier = value
		case "Authority":
			if found.Authority == "" {
				found.Authority = value
			}
		case "Signature":
			found.AdHoc = value == "adhoc"
		}
	}
	return found
}

// SignedBy says whether a signature is identity's (its signing certificate's name) with jst's
// identifier.
func SignedBy(signature Signature, identity string) bool {
	return !signature.AdHoc && signature.Identifier == SigningIdentifier && signature.Authority == identity
}

// Certified says whether a signature is a signing certificate's with jst's identifier: a release's
// binary (signed with a Developer ID and notarised, W-075) or one an install signed. Such a binary
// is installed as it is, whatever identity the setting names: signing it again would change its
// code directory, and Gatekeeper would no longer find its notarisation. Only an unsigned or ad-hoc
// signed binary (a local `go build`), or one with another identifier, is signed at install.
func Certified(signature Signature) bool {
	return !signature.AdHoc && signature.Authority != "" && signature.Identifier == SigningIdentifier
}

// KeepsItsSignature says whether file is Certified and its signature verifies strictly, and what
// its signature is. A display that fails (codesign finds the file unsigned) is no signature to keep.
func KeepsItsSignature(run Runner, file string) (bool, Signature, error) {
	said, err := run(DisplayArgs(file))
	if err != nil {
		return false, Signature{}, fmt.Errorf("codesign --display did not run: %w", err)
	}
	if said.Status != 0 {
		return false, Signature{}, nil
	}
	signature := ReadSignature(string(said.Stderr) + string(said.Stdout))
	if !Certified(signature) {
		return false, signature, nil
	}
	verified, err := run(VerifyArgs(file))
	if err != nil {
		return false, signature, fmt.Errorf("codesign --verify did not run: %w", err)
	}
	return verified.Status == 0, signature, nil
}

// SigningFix is what to do when the binary cannot be signed with identity, the setting's at path.
func SigningFix(identity, path string) string {
	return fmt.Sprintf("put %q back in the keychain (`security find-identity -v -p codesigning` lists the valid ones), "+
		"or write another identity's name into %s, or remove that file to install without signing; "+
		"`jst doctor` checks the setting (docs/design/implementation.md section 7.4)", identity, path)
}

// IdentityValid says whether `security find-identity -v -p codesigning` lists identity among the
// valid (present, unexpired, unrevoked) code-signing identities, by its exact name. A line whose
// quoted name is followed by anything (`"…" (CSSMERR_TP_CERT_REVOKED)`, which -v still lists) is not
// valid.
func IdentityValid(listing, identity string) bool {
	for _, line := range strings.Split(listing, "\n") {
		number, rest, found := strings.Cut(strings.TrimSpace(line), ") ")
		if !found || strings.TrimLeft(number, "0123456789") != "" || number == "" {
			continue
		}
		_, quoted, _ := strings.Cut(rest, " ")
		if !strings.HasPrefix(quoted, `"`) || !strings.HasSuffix(quoted, `"`) || len(quoted) < 2 {
			continue
		}
		name := quoted[1 : len(quoted)-1]
		if name == identity {
			return true
		}
	}
	return false
}

// Runner asks a program a question (supervise.Capture; tests give their own).
type Runner func(argv []string) (supervise.Captured, error)

// realRunner runs argv through supervise under signTimeout.
func realRunner(argv []string) (supervise.Captured, error) {
	ctx, cancel := context.WithTimeout(context.Background(), signTimeout)
	defer cancel()
	return supervise.Capture(ctx, argv, "/", os.Environ(), nil)
}

// Sign signs file with identity, then verifies it strictly and reads the signature back: an error
// names the step that failed, and the caller refuses the install.
func Sign(run Runner, identity, file string) error {
	step := func(what string, argv []string) (supervise.Captured, error) {
		said, err := run(argv)
		if err != nil {
			return said, fmt.Errorf("%s did not run: %w", what, err)
		}
		if said.Status != 0 {
			return said, fmt.Errorf("%s failed (exit %d): %s", what, said.Status, firstLine(said))
		}
		return said, nil
	}
	if _, err := step("codesign --sign", SignArgs(identity, file)); err != nil {
		return err
	}
	if _, err := step("codesign --verify", VerifyArgs(file)); err != nil {
		return err
	}
	said, err := step("codesign --display", DisplayArgs(file))
	if err != nil {
		return err
	}
	if signature := ReadSignature(string(said.Stderr) + string(said.Stdout)); !SignedBy(signature, identity) {
		return fmt.Errorf("the signature is not %q with the identifier %s (identifier %q, authority %q)",
			identity, SigningIdentifier, signature.Identifier, signature.Authority)
	}
	return nil
}

func firstLine(said supervise.Captured) string {
	text := strings.TrimSpace(string(said.Stderr))
	if text == "" {
		text = strings.TrimSpace(string(said.Stdout))
	}
	line, _, _ := strings.Cut(text, "\n")
	return line
}

// resignPartial is the folder inside a live version folder where its binary is signed again: a
// `.partial` name (layout), removed when the signing ends and before the next one starts.
const resignPartial = "resign" + layout.PartialSuffix

// Replacement is what Resign did to the live binary.
type Replacement int

const (
	// LeftAlone means the live binary keeps its signature.
	LeftAlone Replacement = iota
	// SignedAgain means a copy of the binary being installed was signed and put in its place.
	SignedAgain
	// ReplacedAsSigned means the binary being installed keeps its own signature (a release's), so it was
	// put in its place unsigned by this machine.
	ReplacedAsSigned
)

// Resign replaces the binary of the live version folder when it does not keep its signature
// (KeepsItsSignature: it is unsigned or ad-hoc, was installed before the setting, has an earlier
// identifier (W-075) or does not verify strictly), and says what it did. The copy that replaces it
// is signed with identity unless it keeps its own signature: a release's binary installed over an
// ad-hoc build of the same version keeps its notarisation. candidate is the binary being installed, which names the live version
// and commit: it, not the live file, is what is copied. The live file is never signed in place: the
// copy in the folder is signed, verified and read back (Sign), proved through its own links as a
// new folder is (prove.Folder), and only then renamed over the live binary, so an error leaves the
// live binary as it was.
func Resign(ctx context.Context, run Runner, identity, folder, candidate, versionText string) (Replacement, error) {
	live := filepath.Join(folder, layout.Program)
	if kept, _, err := KeepsItsSignature(run, live); err != nil || kept {
		return LeftAlone, err
	}
	work := filepath.Join(folder, resignPartial)
	if err := os.RemoveAll(work); err != nil {
		return LeftAlone, err
	}
	if err := os.Mkdir(work, 0o755); err != nil { // #nosec G301 -- read by every shell of the account
		return LeftAlone, err
	}
	defer func() { _ = os.RemoveAll(work) }()
	signed := filepath.Join(work, layout.Program)
	if err := layout.CopyBinary(candidate)(signed); err != nil {
		return LeftAlone, fmt.Errorf("%s could not be copied: %w", candidate, err)
	}
	if err := os.Chmod(signed, 0o755); err != nil { // #nosec G302 -- an executable for every shell of the account
		return LeftAlone, err
	}
	for _, name := range layout.Launchers {
		if name != layout.Program {
			if err := os.Symlink(layout.Program, filepath.Join(work, name)); err != nil {
				return LeftAlone, err
			}
		}
	}
	done := SignedAgain
	kept, _, err := KeepsItsSignature(run, signed)
	switch {
	case err != nil:
		return LeftAlone, err
	case kept:
		done = ReplacedAsSigned
	default:
		if err := Sign(run, identity, signed); err != nil {
			return LeftAlone, err
		}
	}
	if err := prove.Folder(ctx, work, versionText); err != nil {
		return LeftAlone, fmt.Errorf("the copy does not work: %w", err)
	}
	if err := os.Rename(signed, live); err != nil {
		return LeftAlone, err
	}
	return done, nil
}
