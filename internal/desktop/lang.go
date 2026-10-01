package desktop

import (
	"os"
	"strings"
)

// SystemLanguage returns the operating system's UI language as an
// Accept-Language style tag ("de-DE"), or "" when unknown. The shell's own
// texts (tray menu) follow it when no UI language is configured; the window
// gets the same from the WebView's Accept-Language.
func SystemLanguage() string {
	if l := systemLanguage(); l != "" {
		return l
	}
	for _, v := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l := os.Getenv(v); l != "" && l != "C" && l != "POSIX" {
			return posixToTag(l)
		}
	}
	return ""
}

// posixToTag turns "de_DE.UTF-8" into "de-DE".
func posixToTag(l string) string {
	if i := strings.IndexAny(l, ".@"); i >= 0 {
		l = l[:i]
	}
	return strings.ReplaceAll(l, "_", "-")
}
