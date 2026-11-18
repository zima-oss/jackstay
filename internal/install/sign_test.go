package install

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/zima-oss/jackstay/internal/supervise"
)

const owner = "Apple Development: Max Example (ABCDE12345)"

// What codesign --display --verbose=2 prints (on standard error) for a signed and an ad-hoc binary.
const signedDisplay = `Executable=/p/lib/jackstay-0.5.4-abc-1-1/jst
Identifier=jst
Format=Mach-O thin (arm64)
CodeDirectory v=20500 size=9999 flags=0x10000(runtime) hashes=300+2 location=embedded
Signature size=4790
Authority=` + owner + `
Authority=Apple Worldwide Developer Relations Certification Authority
Authority=Apple Root CA
TeamIdentifier=ABCDE12345
`

const adHocDisplay = `Executable=/p/lib/jackstay-0.5.3-abc-1-1/jst
Identifier=a.out
Format=Mach-O thin (arm64)
CodeDirectory v=20400 size=9999 flags=0x20002(adhoc,linker-signed) hashes=300+0 location=embedded
Signature=adhoc
TeamIdentifier=not set
`

func TestReadSignatureAndSignedBy(t *testing.T) {
	signed := ReadSignature(signedDisplay)
	if signed != (Signature{Identifier: SigningIdentifier, Authority: owner}) || !SignedBy(signed, owner) {
		t.Fatalf("signed %+v", signed)
	}
	if SignedBy(signed, "Apple Development: Someone Else (ZZZZZ99999)") {
		t.Fatal("another identity's signature taken for the owner's")
	}
	if SignedBy(signed, strings.Repeat("A1", 20)) {
		t.Fatal("a SHA-1 is not an identity's name: no signature matches it")
	}
	adHoc := ReadSignature(adHocDisplay)
	if !adHoc.AdHoc || adHoc.Identifier != "a.out" || SignedBy(adHoc, owner) {
		t.Fatalf("ad-hoc %+v", adHoc)
	}
}

func TestIdentityValidReadsTheKeychainListing(t *testing.T) {
	listing := `  1) 0123456789ABCDEF0123456789ABCDEF01234567 "` + owner + `"
  3) FEDCBA9876543210FEDCBA9876543210FEDCBA98 "Apple Development: Revoked One (ABCDE12345)" (CSSMERR_TP_CERT_REVOKED)
  2) 89ABCDEF0123456789ABCDEF0123456789ABCDEF "Apple Distribution: Max Example (ABCDE12345)"
     2 valid identities found
`
	if !IdentityValid(listing, owner) || IdentityValid(listing, "0123456789abcdef0123456789abcdef01234567") ||
		IdentityValid(listing, "Apple Development: Max") {
		t.Fatal("the identity is its exact name only")
	}
	if IdentityValid(listing, "Apple Development: Revoked One (ABCDE12345)") || IdentityValid(listing, "FEDCBA9876543210FEDCBA9876543210FEDCBA98") {
		t.Fatal("a revoked identity, which -v still lists, was taken for valid")
	}
	if IdentityValid(listing, "Developer ID Application: Max Example (ABCDE12345)") || IdentityValid("     0 valid identities found\n", owner) {
		t.Fatal("an identity that is not listed was taken for valid")
	}
}

func TestSignArgsAreTheRuling(t *testing.T) {
	want := []string{"codesign", "--sign", owner, "--identifier", "jst", "--options", "runtime", "--timestamp", "--force", "/f/jst"}
	if got := SignArgs(owner, "/f/jst"); !slices.Equal(got, want) {
		t.Fatalf("%q", got)
	}
	if got := VerifyArgs("/f/jst"); !slices.Equal(got, []string{"codesign", "--verify", "--strict", "/f/jst"}) {
		t.Fatalf("%q", got)
	}
}

// fakeCodesign answers each step: the step named in fail fails, display says what display holds.
func fakeCodesign(fail, display string, calls *[][]string) Runner {
	return func(argv []string) (supervise.Captured, error) {
		*calls = append(*calls, argv)
		step := argv[1]
		switch {
		case step == fail && fail == "--display":
			return supervise.Captured{}, errors.New("cannot start")
		case step == fail:
			return supervise.Captured{Status: 1, Stderr: []byte("errSecInternalComponent\n")}, nil
		case step == "--display":
			return supervise.Captured{Stderr: []byte(display)}, nil
		}
		return supervise.Captured{}, nil
	}
}

func TestSignSignsVerifiesAndReadsBack(t *testing.T) {
	var calls [][]string
	if err := Sign(fakeCodesign("", signedDisplay, &calls), owner, "/f/jst"); err != nil || len(calls) != 3 {
		t.Fatalf("%v after %q", err, calls)
	}
	for _, tc := range []struct{ fail, display, said string }{
		{"--sign", signedDisplay, "codesign --sign failed (exit 1): errSecInternalComponent"},
		{"--verify", signedDisplay, "codesign --verify failed"},
		{"--display", signedDisplay, "codesign --display did not run"},
		{"", adHocDisplay, "the signature is not"},
	} {
		calls = nil
		err := Sign(fakeCodesign(tc.fail, tc.display, &calls), owner, "/f/jst")
		if err == nil || !strings.Contains(err.Error(), tc.said) {
			t.Errorf("fail %q: %v", tc.fail, err)
		}
	}
}

func TestTheSettingIsAFileOfThisMachinesHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("JACKSTAY_HOME", home)
	place := filepath.Join(home, ".all", IdentityFile)
	identity, path, err := SigningIdentity()
	if identity != "" || path != place || err != nil {
		t.Fatalf("no setting: %q %q %v", identity, path, err)
	}
	if err := os.MkdirAll(filepath.Dir(place), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(place, []byte(owner+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	identity, _, err = SigningIdentity()
	if runtime.GOOS != "darwin" {
		if identity != "" || err != nil {
			t.Fatalf("off macOS nothing is signed: %q %v", identity, err)
		}
		return
	}
	if identity != owner || err != nil {
		t.Fatalf("%q %v", identity, err)
	}
	if err := os.WriteFile(place, []byte(owner+"\nanother\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := SigningIdentity(); err == nil {
		t.Fatal("two identities were taken")
	}
}
