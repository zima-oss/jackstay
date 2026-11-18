package install

// installHelp is `jst install --help`.
const installHelp = usage + `

  --prefix DIR     install under DIR (default ~/.local): DIR/lib/jackstay-<id>, made live in one rename
  --checkout DIR   build the binary from that jackstay checkout
  --binary FILE    install that built binary
Without --checkout or --binary: the checkout around the working folder, else the running binary.

On a Mac whose ~/.jackstay/.all/codesign-identity names a code-signing identity (one line, the name
as ` + "`security find-identity -v -p codesigning`" + ` prints it), a new binary that is unsigned or ad-hoc
signed (a local build) is signed with it and the identifier jst before it becomes live, and verified;
a failure refuses the install and names the fix. A binary already signed by a certificate with the
identifier jst whose signature verifies strictly (a release's, notarised) is kept as it is: signing it
again would lose its notarisation.
An install of the version already live whose binary does not keep its signature that way (installed
before the setting, an earlier identifier, or a signature that does not verify) signs it again: a
copy of the binary being installed is signed in its folder, verified, proved and renamed over it.
Without that file, and off macOS, the binary keeps the signature it has.

exit: 0 installed; 1 a step failed half-way (the message says what is left); 2 refused, nothing
was changed.

example: jst install --checkout ~/repositories/jackstay
`

// uninstallHelp is `jst uninstall --help`.
const uninstallHelp = `usage: jst uninstall [--prefix DIR]

  --prefix DIR   remove what install wrote under DIR (default ~/.local); ~/.jackstay is left

exit: 0 removed, or nothing was installed there; 1 something could not be removed (each named); 2 a
bad request, a held install lock or a prefix that cannot be read: nothing changed.

example: jst uninstall
`
