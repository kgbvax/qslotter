package sync

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/store"
)

// Clublog answers bad credentials - or a banned IP - with 403 and bans an IP
// that keeps trying. After a 403 nothing logs in automatically (background
// loop, startup check) until the credentials change or a login by hand gets
// through (Pull/Push button, the Settings check). The refusal is kept in the
// store, so a restart does not try again either.
const (
	metaRefusedCreds = "clublog_refused_creds" // fingerprint of the refused credentials
	metaRefusedAt    = "clublog_refused_at"
)

// fingerprint identifies a set of credentials without storing the secrets.
func fingerprint(c *clublog.Client) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{c.Email, c.AppPassword, c.Call, c.APIKey}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// NoteLogin records the outcome of a Clublog request: a 403 pauses the
// automatic logins with these credentials, a success ends the pause. Other
// failures (network, HTTP 5xx) say nothing about the credentials.
func NoteLogin(st store.Store, c *clublog.Client, err error) {
	switch {
	case errors.Is(err, clublog.ErrForbidden):
		_ = st.MetaSet(metaRefusedCreds, fingerprint(c))
		_ = st.MetaSet(metaRefusedAt, time.Now().UTC().Format(time.RFC3339))
	case err == nil:
		if fp, _ := st.MetaGet(metaRefusedCreds); fp != "" {
			_ = st.MetaSet(metaRefusedCreds, "")
			_ = st.MetaSet(metaRefusedAt, "")
		}
	}
}

// RefusedAt returns when Clublog refused these credentials with a 403 (RFC
// 3339), or "" when it did not or the credentials changed since.
func RefusedAt(st store.Store, c *clublog.Client) string {
	if c == nil {
		return ""
	}
	if fp, _ := st.MetaGet(metaRefusedCreds); fp == "" || fp != fingerprint(c) {
		return ""
	}
	at, _ := st.MetaGet(metaRefusedAt)
	return at
}
