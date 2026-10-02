// Package sync orchestrates the Clublog pull, ADIF parse, diff against the
// local store, and upsert of changed QSOs. Push-back is a separate entry point.
package sync

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/adif"
	"github.com/dl9et/qslotter/internal/clublog"
	"github.com/dl9et/qslotter/internal/events"
	"github.com/dl9et/qslotter/internal/qualify"
	"github.com/dl9et/qslotter/internal/store"
)

type Orchestrator struct {
	Store   store.Store
	Clublog *clublog.Client
	Rules   *qualify.Rules // optional; if set, PullAndUpsert also enqueues eligible QSOs
	Broker  *events.Broker // optional; announces queue changes to open windows

	// Configure, if set, is called before each background pull/push: it
	// returns a client built from the live config (credentials changed under
	// Settings apply at once) and false while no credentials are set (the
	// tick is skipped - no failed logins).
	Configure func() (*clublog.Client, bool)

	// OnNewQSO, if set, is told about every QSO the pull added (a card
	// written during that QSO gets booked, package contact).
	OnNewQSO func(*store.QSO)
}

// configure refreshes the client for a background run; false = skip it.
func (o *Orchestrator) configure() bool {
	if o.Configure == nil {
		return true
	}
	c, ok := o.Configure()
	if !ok {
		return false
	}
	o.Clublog = c
	return true
}

func (o *Orchestrator) announce(key, to string) {
	if o.Broker != nil {
		o.Broker.Publish(events.QueueChanged(key, to))
	}
}

// PullAndUpsert fetches the full log from Clublog, parses it, and upserts each
// QSO into the local store. Returns counts of inserted/updated and any error.
// It is safe to run periodically (every pull_interval).
func (o *Orchestrator) PullAndUpsert() (inserted, updated int, err error) {
	adifBytes, err := o.Clublog.PullLog("")
	NoteLogin(o.Store, o.Clublog, err)
	if err != nil {
		return 0, 0, err
	}
	recs, err := adif.NewReader(bytes.NewReader(adifBytes)).ReadAll()
	if err != nil {
		return 0, 0, fmt.Errorf("parse adif: %w", err)
	}
	// Clublog can hold two records with one key (same call, minute and band,
	// e.g. an FT4 and an MFSK entry for one QSO). Upserting both would flip the
	// stored row twice per pull and count two updates, so only the last record
	// of a key is used - the state the row ended in before anyway.
	var qsos []*store.QSO
	last := make(map[string]int, len(recs))
	dups := 0
	for _, rec := range recs {
		q, err := toQSO(rec)
		if err != nil {
			// Skip unparseable records but continue.
			continue
		}
		if i, ok := last[q.QSLKey]; ok {
			qsos[i] = nil
			dups++
		}
		last[q.QSLKey] = len(qsos)
		qsos = append(qsos, q)
	}
	if dups > 0 {
		log.Printf("[pull] %d QSOs have several records in the Clublog export (same call, minute, band); using the last", dups)
	}
	logged := 0
	for _, q := range qsos {
		if q == nil {
			continue
		}
		var before *store.QSO
		if logged < 10 {
			before, _ = o.Store.GetQSO(q.QSLKey)
		}
		isNew, changed, err := o.Store.UpsertQSO(q)
		if err != nil {
			return inserted, updated, err
		}
		if isNew {
			inserted++
			if o.OnNewQSO != nil {
				o.OnNewQSO(q)
			}
		} else if changed {
			updated++
			if before != nil && logged < 10 {
				logged++
				log.Printf("[pull] updated %s: %s", q.QSLKey, diffQSO(before, q))
			}
		}
		// The card already went out through another tool: an open queue item
		// for it is done (a duplicate card costs more than a wrong auto-close).
		if (isNew || changed) && q.SentPerLog() {
			if err := o.Store.QueueCloseSentElsewhere(q.QSLKey); err == nil {
				o.announce(q.QSLKey, "sent")
			}
		}
	}
	_ = o.Store.MetaSet("clublog_last_pull_at", time.Now().UTC().Format(time.RFC3339))
	// If rules are configured, run the qualifier to enqueue any newly-eligible
	// QSOs. This catches UDP-missed QSOs and QSL-state edits that flip a QSO
	// from "sent" back to "not sent" (rare but possible).
	if o.Rules != nil {
		keys, err := o.Rules.EnqueueAllKeys(o.Store)
		for _, k := range keys {
			o.announce(k, "queued")
		}
		if err != nil {
			return inserted, updated, fmt.Errorf("enqueue: %w", err)
		}
	}
	return inserted, updated, nil
}

// PushBack uploads all QSOs whose local QSL state diverges from Clublog's view
// (i.e. qslotter has marked them sent/received but Clublog doesn't know yet).
func (o *Orchestrator) PushBack() (pushed int, err error) {
	pending, err := o.Store.PendingPushBack()
	if err != nil {
		return 0, err
	}
	if len(pending) == 0 {
		return 0, nil
	}
	var buf bytes.Buffer
	w := adif.NewWriter(&buf)
	for _, q := range pending {
		rec := fromQSO(q)
		// Override QSL_SENT/QSL_RCVD with the local value, set date. The send
		// route uses the ADIF fields: QSL_SENT_VIA = how the card travelled
		// (bureau/direct), plus QSL_VIA = the manager's callsign for a manager
		// card (ADIF marks QSL_SENT_VIA=M import-only). Cards from older
		// builds may carry "M" (QSL_VIA alone) or no route (QSL_SENT=Y alone).
		if q.QSLSentLocal.Valid && q.QSLSentLocal.String != "" {
			rec.Set("QSL_SENT", q.QSLSentLocal.String)
			method := strings.ToUpper(q.QSLSentMethodLocal.String)
			switch method {
			case "B", "D", "E":
				rec.Set("QSL_SENT_VIA", method)
			}
			if method == "B" || method == "D" || method == "M" {
				if item, _ := o.Store.QueueGet(q.QSLKey); item != nil && item.DesiredMethod == "M" && item.Manager != "" {
					rec.Set("QSL_VIA", item.Manager)
				}
			}
			if q.QSLSDateLocal.Valid && q.QSLSDateLocal.String != "" {
				rec.Set("QSLSDATE", q.QSLSDateLocal.String)
			}
		}
		if q.QSLRcvdLocal.Valid && q.QSLRcvdLocal.String != "" {
			rec.Set("QSL_RCVD", q.QSLRcvdLocal.String)
			if q.QSLRDateLocal.Valid && q.QSLRDateLocal.String != "" {
				rec.Set("QSLRDATE", q.QSLRDateLocal.String)
			}
		}
		if err := w.Write(rec); err != nil {
			return pushed, err
		}
	}
	err = o.Clublog.PushLogs(buf.Bytes())
	NoteLogin(o.Store, o.Clublog, err)
	if err != nil {
		return pushed, err
	}
	for _, q := range pending {
		if err := o.Store.MarkPushed(q); err != nil {
			return pushed, err
		}
		pushed++
	}
	_ = o.Store.MetaSet("clublog_last_push_at", time.Now().UTC().Format(time.RFC3339))
	return pushed, nil
}

// ToQSOFromRecord converts an ADIF record to a store.QSO with a stable hash.
// Exported so the UDP listener can build a QSO from a datagram without
// duplicating the field-mapping logic.
func ToQSOFromRecord(rec adif.Record) (*store.QSO, error) {
	return toQSO(rec)
}

// toQSO converts an ADIF record to a store.QSO with a stable hash.
func toQSO(rec adif.Record) (*store.QSO, error) {
	key, err := rec.QSLKey()
	if err != nil {
		return nil, err
	}
	t, err := rec.QSOTime()
	if err != nil {
		return nil, err
	}
	q := &store.QSO{
		QSLKey:      key,
		Call:        rec.Call(),
		QSODate:     t.Format("20060102"),
		TimeOn:      t.Format("150405"),
		Band:        rec.Get("BAND"),
		Mode:        rec.Get("MODE"),
		Freq:        rec.Get("FREQ"),
		RSTSent:     rec.Get("RST_SENT"),
		RSTRcvd:     rec.Get("RST_RCVD"),
		QSLSent:     rec.Get("QSL_SENT"),
		QSLRcvd:     rec.Get("QSL_RCVD"),
		QSLSDate:    rec.Get("QSLSDATE"),
		QSLRDate:    rec.Get("QSLRDATE"),
		LoTWQSLRcvd: rec.Get("LOTW_QSL_RCVD"),
		DXCC:        rec.Get("DXCC"),
		PropMode:    rec.Get("PROP_MODE"),
		Gridsquare:  rec.Get("GRIDSQUARE"),
		Operator:    rec.Get("OPERATOR"),
		Notes:       rec.Get("NOTES"),
		Name:        rec.Get("NAME"),
		QTH:         rec.Get("QTH"),
		Hash:        hashRecord(rec),
	}
	return q, nil
}

// fromQSO rebuilds an ADIF record from a store.QSO for push-back.
func fromQSO(q *store.QSO) adif.Record {
	rec := adif.Record{}
	rec.Set("CALL", q.Call)
	rec.Set("QSO_DATE", q.QSODate)
	rec.Set("TIME_ON", q.TimeOn)
	rec.Set("BAND", q.Band)
	rec.Set("MODE", q.Mode)
	if q.Freq != "" {
		rec.Set("FREQ", q.Freq)
	}
	if q.RSTSent != "" {
		rec.Set("RST_SENT", q.RSTSent)
	}
	if q.RSTRcvd != "" {
		rec.Set("RST_RCVD", q.RSTRcvd)
	}
	if q.QSLSent != "" {
		rec.Set("QSL_SENT", q.QSLSent)
	}
	if q.QSLRcvd != "" {
		rec.Set("QSL_RCVD", q.QSLRcvd)
	}
	if q.QSLSDate != "" {
		rec.Set("QSLSDATE", q.QSLSDate)
	}
	if q.QSLRDate != "" {
		rec.Set("QSLRDATE", q.QSLRDate)
	}
	if q.DXCC != "" {
		rec.Set("DXCC", q.DXCC)
	}
	if q.PropMode != "" {
		rec.Set("PROP_MODE", q.PropMode)
	}
	if q.Gridsquare != "" {
		rec.Set("GRIDSQUARE", q.Gridsquare)
	}
	if q.Operator != "" {
		rec.Set("OPERATOR", q.Operator)
	}
	if q.Notes != "" {
		rec.Set("NOTES", q.Notes)
	}
	if q.Name != "" {
		rec.Set("NAME", q.Name)
	}
	if q.QTH != "" {
		rec.Set("QTH", q.QTH)
	}
	return rec
}

// diffQSO names the Clublog-sourced fields that differ between the stored QSO
// and the pulled one (old -> new), for the pull log.
func diffQSO(a, b *store.QSO) string {
	skip := map[string]bool{"QSLKey": true, "Hash": true, "FirstSeenAt": true, "UpdatedAt": true}
	va, vb := reflect.ValueOf(*a), reflect.ValueOf(*b)
	var parts []string
	for i := 0; i < va.NumField(); i++ {
		f := va.Type().Field(i)
		if skip[f.Name] || f.Type.Kind() != reflect.String {
			continue
		}
		if x, y := va.Field(i).String(), vb.Field(i).String(); x != y {
			parts = append(parts, fmt.Sprintf("%s %q -> %q", f.Name, x, y))
		}
	}
	if len(parts) == 0 {
		return "only fields outside the stored columns (hash)"
	}
	return strings.Join(parts, ", ")
}

// hashRecord returns a stable hex hash of the canonical ADIF representation
// of the record (upper-case field names, sorted, with values). Used to detect
// whether a re-pulled QSO changed since the last sync.
func hashRecord(rec adif.Record) string {
	keys := make([]string, 0, len(rec))
	for k := range rec {
		if strings.EqualFold(k, "APP_") || strings.HasPrefix(strings.ToUpper(k), "APP_") {
			continue
		}
		keys = append(keys, strings.ToUpper(k))
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(rec[k])
		sb.WriteByte('|')
	}
	sum := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}
