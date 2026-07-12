package installer

import (
	"fmt"
	"strings"
)

// Label is the launchd service label for the swarmery daemon.
const Label = "com.swarmery.daemon"

// Plist renders the launchd property list for the daemon.
//
// binPath is the installed binary (~/.swarmery/bin/swarmery), logsDir the
// directory for stdout/stderr logs. If port > 0 the plist carries a
// SWARMERY_PORT entry under EnvironmentVariables; otherwise the daemon
// falls back to its built-in default.
func Plist(binPath, logsDir string, port int) string {
	var env string
	if port > 0 {
		env = fmt.Sprintf(`	<key>EnvironmentVariables</key>
	<dict>
		<key>SWARMERY_PORT</key>
		<string>%d</string>
	</dict>
`, port)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>serve</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<true/>
	<key>StandardOutPath</key>
	<string>%s/swarmery.out.log</string>
	<key>StandardErrorPath</key>
	<string>%s/swarmery.err.log</string>
%s</dict>
</plist>
`, Label, binPath, logsDir, logsDir, env)
	return b.String()
}
