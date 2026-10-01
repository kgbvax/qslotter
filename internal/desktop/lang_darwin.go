package desktop

import (
	"os/exec"
	"regexp"
)

var appleLangRe = regexp.MustCompile(`"?([A-Za-z]{2,3}(?:-[A-Za-z0-9]+)*)"?`)

// systemLanguage reads the first of the user's preferred languages.
func systemLanguage() string {
	out, err := exec.Command("defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	// (\n    "de-DE",\n    "en-US"\n)
	if m := appleLangRe.FindSubmatch(out[min(len(out), 1):]); m != nil {
		return string(m[1])
	}
	return ""
}
