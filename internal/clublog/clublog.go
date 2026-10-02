// Package clublog is a minimal client for the Clublog HTTP API.
// It supports two endpoints used by qslotter:
//   - getadif.php  (pull the operator's log as ADIF; full log or OQRS-only)
//   - putlogs.php  (batch upload ADIF; used for QSL-state write-back, where
//     Clublog treats a duplicate QSO as an update for QSL fields)
//
// Auth uses an API key plus the operator's email and an Application Password.
// See https://clublog.freshdesk.com/support/solutions/articles/54910 for keys
// and /3000057171 for Application Passwords.
package clublog

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// ErrForbidden is Clublog's 403: bad credentials or a banned IP. Do not try
// again with the same credentials - Clublog bans an IP that keeps failing.
var ErrForbidden = errors.New("403 forbidden")

type Client struct {
	Email       string
	AppPassword string
	Call        string
	APIKey      string
	HTTP        *http.Client
	BaseURL     string
}

func New(email, appPassword, call, apiKey string) *Client {
	return &Client{
		Email:       email,
		AppPassword: appPassword,
		Call:        call,
		APIKey:      apiKey,
		HTTP:        &http.Client{Timeout: 60 * time.Second},
		BaseURL:     "https://clublog.org",
	}
}

// PullLog fetches the operator's full log as ADIF via getadif.php.
// If recDateFrom is non-empty it must be a "YYYY-MM-DD" string and is passed as
// startyear/startmonth/startday. (Clublog has no true delta endpoint; see
// plan.) Most callers pass "" to fetch the whole log and diff locally.
func (c *Client) PullLog(recDateFrom string) ([]byte, error) {
	form := url.Values{
		"email":    {c.Email},
		"password": {c.AppPassword},
		"call":     {c.Call},
		"api":      {c.APIKey},
	}
	if len(recDateFrom) == 10 && recDateFrom[4] == '-' && recDateFrom[7] == '-' {
		form.Set("startyear", recDateFrom[0:4])
		form.Set("startmonth", recDateFrom[5:7])
		form.Set("startday", recDateFrom[8:10])
	}
	req, err := http.NewRequest("POST", c.BaseURL+"/getadif.php", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("clublog pull: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("clublog pull: %w (bad credentials or IP banned - stop immediately)", ErrForbidden)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("clublog pull: HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// PushLogs uploads an ADIF file via putlogs.php. Clublog processes uploads
// asynchronously (queued; visible 5-60s later). A duplicate QSO with updated
// QSL_SENT/QSL_RCVD updates the existing record's QSL fields.
func (c *Client) PushLogs(adif []byte) error {
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	writeField := func(name, value string) error {
		w, err := mw.CreateFormField(name)
		if err != nil {
			return err
		}
		_, err = w.Write([]byte(value))
		return err
	}
	for _, f := range []struct{ k, v string }{
		{"email", c.Email},
		{"password", c.AppPassword},
		{"callsign", c.Call},
		{"api", c.APIKey},
	} {
		if err := writeField(f.k, f.v); err != nil {
			return fmt.Errorf("clublog push: %w", err)
		}
	}
	// file field
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="qslotter_pushback.adi"`)
	h.Set("Content-Type", "application/octet-stream")
	fw, err := mw.CreatePart(h)
	if err != nil {
		return fmt.Errorf("clublog push: %w", err)
	}
	if _, err := fw.Write(adif); err != nil {
		return fmt.Errorf("clublog push: %w", err)
	}
	if err := mw.Close(); err != nil {
		return fmt.Errorf("clublog push: %w", err)
	}
	req, err := http.NewRequest("POST", c.BaseURL+"/putlogs.php", body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("clublog push: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return fmt.Errorf("clublog push: %w (bad credentials or IP banned - stop immediately)", ErrForbidden)
	}
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("clublog push: HTTP %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}

// CheckCredentials verifies email/app-password/call/api-key with a minimal
// getadif.php request (date range = today, so the response is tiny). A 200
// means the credentials are accepted; 403 means bad credentials (or a banned
// IP, which the error text names). Used at startup and by the settings page.
func (c *Client) CheckCredentials() error {
	today := time.Now().UTC().Format("2006-01-02")
	_, err := c.PullLog(today)
	if errors.Is(err, ErrForbidden) {
		return fmt.Errorf("clublog: %w - credentials rejected (email/app password/API key/call wrong, or IP banned)", ErrForbidden)
	}
	return err
}
