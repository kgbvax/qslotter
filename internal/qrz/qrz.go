// Package qrz is a minimal client for the QRZ XML interface. It logs in,
// caches the session key, performs callsign lookups, and fetches bio HTML.
// Results are cached in qslotter's store (see internal/store).
//
// Spec: https://www.qrz.com/docs/xml/current_spec.html
// Requires a paid QRZ XML Logbook Data subscription.
package qrz

import (
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	Username string
	Password string
	Agent    string
	BaseURL  string
	HTTP     *http.Client

	// Cached session state
	sessionKey  string
	countUsed   int
	subExp      string
	lastLoginAt time.Time
}

func New(username, password, agent string) *Client {
	return &Client{
		Username: username,
		Password: password,
		Agent:    agent,
		BaseURL:  "https://xmldata.qrz.com/xml/current/",
		HTTP:     &http.Client{Timeout: 30 * time.Second},
	}
}

// qrzSession is the top-level XML response from a login or lookup call.
type qrzSession struct {
	XMLName xml.Name `xml:"QRZDatabase"`
	Session struct {
		Key     string `xml:"Key"`
		Count   string `xml:"Count"`
		SubExp  string `xml:"SubExp"`
		Message string `xml:"Message"`
		Error   string `xml:"Error"`
	} `xml:"Session"`
	Callsign *Callsign `xml:"Callsign,omitempty"`
}

// Callsign is the structured station info returned by QRZ.
type Callsign struct {
	Call    string `xml:"call" json:"call"`
	FName   string `xml:"fname" json:"fname"`
	Name    string `xml:"name" json:"name"`
	Attn    string `xml:"attn" json:"attn"`
	Addr1   string `xml:"addr1" json:"addr1"`
	Addr2   string `xml:"addr2" json:"addr2"`
	State   string `xml:"state" json:"state"`
	Zip     string `xml:"zip" json:"zip"`
	Country string `xml:"country" json:"country"`
	DXCC    string `xml:"dxcc" json:"dxcc"`
	Email   string `xml:"email" json:"email"`
	QSLMgr  string `xml:"qslmgr" json:"qslmgr"`
	EQSL    string `xml:"eqsl" json:"eqsl"`
	MQSL    string `xml:"mqsl" json:"mqsl"`
	LoTW    string `xml:"lotw" json:"lotw"`
	Grid    string `xml:"grid" json:"grid"`
	Bio     struct {
		Size int    `xml:"size,attr" json:"size"`
		Date string `xml:"date,attr" json:"date"`
	} `xml:"bio"`
	URL string `xml:"url" json:"url"`
}

// ensureSession logs in if we don't have a usable session key.
func (c *Client) ensureSession() error {
	if c.sessionKey != "" && time.Since(c.lastLoginAt) < 30*time.Minute {
		return nil
	}
	u := fmt.Sprintf("%s?username=%s;password=%s;agent=%s",
		c.BaseURL, c.Username, c.Password, c.Agent)
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return fmt.Errorf("qrz login: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var s qrzSession
	if err := xml.Unmarshal(body, &s); err != nil {
		return fmt.Errorf("qrz login parse: %w", err)
	}
	if s.Session.Error != "" {
		return fmt.Errorf("qrz login error: %s", s.Session.Error)
	}
	if s.Session.Key == "" {
		return fmt.Errorf("qrz login: no session key (message: %s)", s.Session.Message)
	}
	c.sessionKey = s.Session.Key
	c.subExp = s.Session.SubExp
	c.lastLoginAt = time.Now()
	return nil
}

// CheckCredentials verifies the username/password by logging in. It returns
// nil and caches the session on success; the error carries QRZ's message
// (e.g. "password/username incorrect") on failure. Used at startup and by the
// settings page so bad credentials surface immediately.
func (c *Client) CheckCredentials() error { return c.ensureSession() }

// Lookup performs a callsign lookup and returns the structured station data.
func (c *Client) Lookup(callsign string) (*Callsign, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s?s=%s;callsign=%s", c.BaseURL, c.sessionKey, strings.ToUpper(callsign))
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return nil, fmt.Errorf("qrz lookup: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var s qrzSession
	if err := xml.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("qrz lookup parse: %w", err)
	}
	if s.Session.Error != "" {
		// Session expired: retry once
		if strings.Contains(strings.ToLower(s.Session.Error), "session") {
			c.sessionKey = ""
			return c.Lookup(callsign)
		}
		return nil, fmt.Errorf("qrz lookup: %s", s.Session.Error)
	}
	c.countUsed = parseCount(s.Session.Count)
	if s.Callsign == nil {
		return nil, nil
	}
	return s.Callsign, nil
}

// FetchBio retrieves the HTML bio page for a callsign. The returned bytes are
// raw HTML (with embedded CSS); callers strip tags and parse visible text.
func (c *Client) FetchBio(callsign string) (string, error) {
	if err := c.ensureSession(); err != nil {
		return "", err
	}
	u := fmt.Sprintf("%s?s=%s;html=%s", c.BaseURL, c.sessionKey, strings.ToUpper(callsign))
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return "", fmt.Errorf("qrz bio: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return stripHTML(string(body)), nil
}

// CountUsed returns the number of lookups performed in the current 24h window
// (per QRZ's <Count> field). Useful for rate-limit backoff.
func (c *Client) CountUsed() int { return c.countUsed }

func parseCount(s string) int {
	n := 0
	for _, ch := range s {
		if ch >= '0' && ch <= '9' {
			n = n*10 + int(ch-'0')
		}
	}
	return n
}

// stripHTML does a coarse HTML-to-text conversion. Good enough for QSL-bio
// heuristic parsing; not a general-purpose converter.
func stripHTML(s string) string {
	var out strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			out.WriteRune(r)
		}
	}
	return strings.TrimSpace(out.String())
}
