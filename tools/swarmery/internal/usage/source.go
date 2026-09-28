package usage

// SourceForAccount is the one spelling of "which usage Source reads account
// key". The DEFAULT account gets an EMPTY ConfigDir on purpose: that selects
// the legacy resolution chain (CLAUDE_CONFIG_DIR, ~/.claude, ~/.config/claude
// and the plain keychain item on darwin), which on macOS is the only source
// that resolves the stock account — its credential lives in the login Keychain
// and has no file. Naming its dir would switch resolution to the exclusive
// scoped lookup and report the primary login as disconnected. Every other
// account names its own config dir.
//
// The default Source also sets IgnoreConfigDirEnv: the default account is the
// one `claude` runs with CLAUDE_CONFIG_DIR unset, so an inherited value in the
// caller's environment must not attach another account's credential to it.
func SourceForAccount(key, configDir string, isDefault bool) Source {
	src := Source{Account: key}
	if isDefault {
		src.IgnoreConfigDirEnv = true
	} else {
		src.ConfigDir = configDir
	}
	return src
}
