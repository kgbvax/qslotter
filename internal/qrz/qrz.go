// Package qrz is a minimal client for the QRZ XML interface. It logs in,
// caches the session key, performs callsign lookups, and fetches bio HTML.
// Results are cached in qslotter's store (see internal/store).
//
// Spec: https://www.qrz.com/docs/xml/current_spec.html
// Requires a paid QRZ XML Logbook Data subscription.
package qrz

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Client struct {
	Username string
	Password string
	Agent    string
	BaseURL  string
	HTTP     *http.Client

	// Cached session state. Lookups run from concurrent goroutines (web
	// renders, the UDP feed), so mu guards these fields.
	mu          sync.Mutex
	sessionKey  string
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionKey != "" && time.Since(c.lastLoginAt) < 30*time.Minute {
		return nil
	}
	u := fmt.Sprintf("%s?username=%s;password=%s;agent=%s",
		c.BaseURL, url.QueryEscape(c.Username), url.QueryEscape(c.Password), url.QueryEscape(c.Agent))
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return fmt.Errorf("qrz login: %w", redact(err))
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

// key returns the current session key.
func (c *Client) key() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionKey
}

// Lookup performs a callsign lookup and returns the structured station data.
// A callsign QRZ has no record of yields (nil, nil): that is an answer, not an
// error.
func (c *Client) Lookup(callsign string) (*Callsign, error) {
	return c.lookup(callsign, true)
}

func (c *Client) lookup(callsign string, retry bool) (*Callsign, error) {
	if err := c.ensureSession(); err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s?s=%s;callsign=%s", c.BaseURL, c.key(), url.QueryEscape(strings.ToUpper(callsign)))
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return nil, fmt.Errorf("qrz lookup: %w", redact(err))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var s qrzSession
	if err := xml.Unmarshal(body, &s); err != nil {
		return nil, fmt.Errorf("qrz lookup parse: %w", err)
	}
	if s.Session.Error != "" {
		msg := strings.ToLower(s.Session.Error)
		// Session expired: log in again and retry once.
		if retry && strings.Contains(msg, "session") {
			c.mu.Lock()
			c.sessionKey = ""
			c.mu.Unlock()
			return c.lookup(callsign, false)
		}
		if strings.Contains(msg, "not found") {
			return nil, nil // QRZ has no record: a definite answer, worth caching
		}
		return nil, fmt.Errorf("qrz lookup: %s", s.Session.Error)
	}
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
	u := fmt.Sprintf("%s?s=%s;html=%s", c.BaseURL, c.key(), url.QueryEscape(strings.ToUpper(callsign)))
	resp, err := c.HTTP.Get(u)
	if err != nil {
		return "", fmt.Errorf("qrz bio: %w", redact(err))
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return stripHTML(string(body)), nil
}

var (
	styleBlockRe  = regexp.MustCompile(`(?is)<style\b.*?</style>`)
	scriptBlockRe = regexp.MustCompile(`(?is)<script\b.*?</script>`)
	commentRe     = regexp.MustCompile(`(?s)<!--.*?-->`)
	lineBreakRe   = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/tr|/h[1-6])\b[^>]*>`)
	tagRe         = regexp.MustCompile(`<[^>]*>`)
	spacesRe      = regexp.MustCompile(`[ \t\r\f\v\x{a0}]+`)
)

// stripHTML converts a QRZ bio page to readable text: style/script blocks and
// comments are dropped (a QRZ page carries pages of CSS), block tags become
// line breaks, entities are decoded and whitespace is collapsed. Good enough
// for QSL-bio heuristics and display; not a general-purpose converter.
func stripHTML(s string) string {
	s = styleBlockRe.ReplaceAllString(s, " ")
	s = scriptBlockRe.ReplaceAllString(s, " ")
	s = commentRe.ReplaceAllString(s, " ")
	s = lineBreakRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	var lines []string
	for _, ln := range strings.Split(s, "\n") {
		ln = strings.TrimSpace(spacesRe.ReplaceAllString(ln, " "))
		if ln == "" && (len(lines) == 0 || lines[len(lines)-1] == "") {
			continue // collapse runs of blank lines
		}
		lines = append(lines, ln)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// redact drops the request URL from a transport error: the URL carries the
// password (login) or the session key, and errors end up in the log file.
func redact(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return fmt.Errorf("%s: %w", ue.Op, ue.Err)
	}
	return err
}
