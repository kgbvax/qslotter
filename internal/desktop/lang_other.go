//go:build !darwin && !windows

package desktop

func systemLanguage() string { return "" } // the LANG/LC_* environment decides
