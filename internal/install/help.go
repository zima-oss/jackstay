package install

// installHelp is `jst install --help`.
const installHelp = usage + `

Without --checkout or --binary: the checkout around the working folder, else the running binary.
On macOS, when ~/.jackstay/.all/codesign-identity names a code-signing identity (as
` + "`security find-identity -v -p codesigning`" + ` prints it), an unsigned or ad-hoc signed binary is
signed with it (identifier jst) and verified before it goes live; a release's notarised signature
is kept. A live install of the same version without that signature is replaced.
  --prefix DIR     install under DIR (default ~/.local)
  --checkout DIR   build the binary from that jackstay checkout
  --binary FILE    install that built binary

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
