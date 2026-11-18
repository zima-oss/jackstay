// Package installscript holds the tests of install.sh, the quick install at the repository root
// (W-076): against a fake release served on this machine, it installs, refuses an archive whose
// hash does not match checksums.txt, and refuses a platform jackstay has no build for, running
// nothing it downloaded; the README's one-liner, served over TLS, installs only a whole script,
// and the guide, the site and the script's header carry the same line. The tests need real
// processes and are integration tests.
package installscript
