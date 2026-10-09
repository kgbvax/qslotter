package main

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dl9et/qslotter/internal/config"
	"github.com/dl9et/qslotter/internal/qrz"
	"github.com/dl9et/qslotter/internal/store"
	"github.com/dl9et/qslotter/qpc"
)

type sampleMeta struct {
	Seed     uint64   `json:"seed"`
	N        int      `json:"n"`
	DB       string   `json:"db"`
	PoolQSOs int      `json:"pool_qsos"`
	Drawn    int      `json:"drawn_calls"` // distinct calls looked at, incl. skipped
	NotFound []string `json:"not_found"`   // calls QRZ does not know, replaced by the next draw
	Excluded int      `json:"excluded"`    // calls left out because an earlier dataset had them
	Created  string   `json:"created"`
}

func cmdSample(args []string) error {
	fl := flag.NewFlagSet("sample", flag.ExitOnError)
	dir := fl.String("dir", dirFlagDefault(), "working directory")
	db := fl.String("db", "", "COPY of the qslotter database (default <dir>/qslotter.db)")
	cfgPath := fl.String("config", "", "qslotter config.yaml (QRZ credentials only)")
	n := fl.Int("n", 100, "number of distinct stations")
	seed := fl.Uint64("seed", 0, "shuffle seed (0 = random, recorded in dataset.meta.json)")
	pause := fl.Duration("pause", time.Second, "pause between QRZ stations")
	exclude := fl.String("exclude", "", "earlier dataset.jsonl whose calls must not be drawn again (for a fresh sample)")
	out := fl.String("out", "", "output (default <dir>/dataset.jsonl)")
	force := fl.Bool("force", false, "overwrite an existing dataset")
	fl.Parse(args)
	if *db == "" {
		*db = filepath.Join(*dir, "qslotter.db")
	}
	if *out == "" {
		*out = filepath.Join(*dir, "dataset.jsonl")
	}
	if _, err := os.Stat(*out); err == nil && !*force {
		return fmt.Errorf("%s exists (labels may refer to it); use -out or -force", *out)
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if _, err := os.Stat(*db); err != nil {
		return fmt.Errorf("database copy: %w", err)
	}
	qc, err := qrzClient(*cfgPath)
	if err != nil {
		return err
	}

	st, err := store.Open(*db)
	if err != nil {
		return err
	}
	defer st.Close()
	qsos, err := st.AllQSOs()
	if err != nil {
		return err
	}
	if *seed == 0 {
		*seed = rand.Uint64()
	}
	excluded := map[string]bool{}
	if *exclude != "" {
		prev, err := loadDataset(*exclude)
		if err != nil {
			return err
		}
		for _, it := range prev {
			excluded[store.BaseCall(it.Call)] = true
		}
	}
	order := drawOrder(qsos, *seed)

	meta := sampleMeta{Seed: *seed, N: *n, DB: *db, PoolQSOs: len(qsos), Created: time.Now().UTC().Format(time.RFC3339)}
	var items []item
	for _, q := range order {
		if len(items) == *n {
			break
		}
		if excluded[store.BaseCall(q.Call)] {
			meta.Excluded++
			continue
		}
		meta.Drawn++
		it, found, err := fetchStation(qc, q)
		if err != nil {
			return fmt.Errorf("%s: %w (nothing written; rerun with -seed %d to continue the same draw)", q.Call, err, *seed)
		}
		if !found {
			meta.NotFound = append(meta.NotFound, q.Call)
			log.Printf("%-12s not on QRZ, skipped", q.Call)
		} else {
			items = append(items, it)
			log.Printf("%3d/%d %-12s bio %5d chars", len(items), *n, q.Call, len(it.Bio))
		}
		time.Sleep(*pause)
	}
	if len(items) < *n {
		log.Printf("only %d stations available", len(items))
	}

	tmp := *out + ".tmp"
	os.Remove(tmp)
	for _, it := range items {
		if err := appendJSONL(tmp, it); err != nil {
			return err
		}
	}
	if err := os.Rename(tmp, *out); err != nil {
		return err
	}
	if err := writeJSON(strings.TrimSuffix(*out, ".jsonl")+".meta.json", meta); err != nil {
		return err
	}
	log.Printf("wrote %d stations to %s (seed %d)", len(items), *out, *seed)
	return nil
}

// qrzClient logs in to QRZ with the credentials from a qslotter config.
func qrzClient(cfgPath string) (*qrz.Client, error) {
	if cfgPath == "" {
		return nil, errors.New("-config (the qslotter config with QRZ credentials) is required")
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	if cfg.QRZ.Username == "" || cfg.QRZ.Password == "" {
		return nil, errors.New("no QRZ credentials in " + cfgPath)
	}
	qc := qrz.New(cfg.QRZ.Username, cfg.QRZ.Password, cfg.QRZ.Agent)
	return qc, qc.CheckCredentials()
}

// cmdEnrich adds the QRZ postal address to an existing dataset without
// touching anything else (bio and QSL fields stay as labelled). Datasets
// sampled before the address became part of qpc.Station need it once.
func cmdEnrich(args []string) error {
	fl := flag.NewFlagSet("enrich", flag.ExitOnError)
	dir := fl.String("dir", dirFlagDefault(), "working directory")
	cfgPath := fl.String("config", "", "qslotter config.yaml (QRZ credentials only)")
	pause := fl.Duration("pause", 500*time.Millisecond, "pause between QRZ lookups")
	fl.Parse(args)
	path := filepath.Join(*dir, "dataset.jsonl")
	items, err := loadDataset(path)
	if err != nil {
		return err
	}
	qc, err := qrzClient(*cfgPath)
	if err != nil {
		return err
	}
	for i := range items {
		it := &items[i]
		call := it.Call
		if it.QRZCall != "" {
			call = it.QRZCall
		}
		cs, err := qc.Lookup(call)
		if err != nil {
			return fmt.Errorf("%s: %w (dataset unchanged)", call, err)
		}
		if cs == nil {
			log.Printf("%-12s no longer on QRZ, address left empty", call)
			continue
		}
		it.Addr1, it.Addr2, it.State, it.Zip = cs.Addr1, cs.Addr2, cs.State, cs.Zip
		full := "full"
		if !it.HasFullAddress() {
			full = "NOT full"
		}
		log.Printf("%3d/%d %-12s address %s", i+1, len(items), it.Call, full)
		time.Sleep(*pause)
	}
	backup := strings.TrimSuffix(path, ".jsonl") + ".before-enrich.jsonl"
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	for _, it := range items {
		if err := appendJSONL(path, it); err != nil {
			return err
		}
	}
	log.Printf("addresses added to %s (previous version: %s)", path, backup)
	return nil
}

// drawOrder shuffles the QSOs with seed and keeps the first QSO of every
// station (by base call): random QSOs, each station at most once. As with
// drawing QSOs, a station's chance grows with its number of QSOs.
func drawOrder(qsos []*store.QSO, seed uint64) []*store.QSO {
	shuffled := append([]*store.QSO(nil), qsos...)
	r := rand.New(rand.NewPCG(seed, 0))
	r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	seen := map[string]bool{}
	var out []*store.QSO
	for _, q := range shuffled {
		b := store.BaseCall(q.Call)
		if b == "" || seen[b] {
			continue
		}
		seen[b] = true
		out = append(out, q)
	}
	return out
}

// fetchStation looks the logged call up on QRZ and fetches the bio of the
// record QRZ returns. found is false when QRZ has no record.
func fetchStation(qc *qrz.Client, q *store.QSO) (item, bool, error) {
	call := strings.ToUpper(strings.TrimSpace(q.Call))
	cs, err := qc.Lookup(call)
	if err != nil {
		return item{}, false, err
	}
	if cs == nil {
		return item{}, false, nil
	}
	bio, err := qc.FetchBio(cs.Call)
	if err != nil {
		return item{}, false, err
	}
	it := item{
		Station: qpc.Station{
			Call: call, Country: cs.Country, DXCC: cs.DXCC, QSLMgr: cs.QSLMgr,
			MQSL: cs.MQSL, EQSL: cs.EQSL, LoTW: cs.LoTW,
			Addr1: cs.Addr1, Addr2: cs.Addr2, State: cs.State, Zip: cs.Zip, Bio: bio,
		},
		QSOKey:    q.QSLKey,
		FetchedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if !strings.EqualFold(cs.Call, call) {
		it.QRZCall = strings.ToUpper(cs.Call)
	}
	return it, true, nil
}
