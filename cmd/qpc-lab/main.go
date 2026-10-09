// Command qpc-lab is the experiment harness for the QSL preference classifier
// (package qpc). It is a calibration tool, not part of the app: it decides
// whether an LLM reads QRZ QSL instructions better than the heuristic in
// internal/qsldetermine (docs/VISION.md E2).
//
// Local-only working layout (-dir, default eval/qpc; /eval/ is git-ignored
// because bios and logs are other people's data):
//
//	qslotter.db(+-wal)   a COPY of the real database (store.Open migrates and
//	                     bumps meta, so never point -db at the live file)
//	dataset.jsonl        the sampled stations (qpc.Station + QSO key), and
//	dataset.meta.json    how they were drawn (seed, pool, skipped calls)
//	gold.jsonl           manual labels, append-only, the last entry per call wins
//	runs/<variant>/      results.jsonl + run.json per classifier variant
//	report.md            the comparison
//	compare.md           two runs side by side, without labels (compare)
//
// Workflow:
//
//	qpc-lab sample -config ../config.yaml -n 100     # QRZ lookups, fresh bios
//	qpc-lab label                                    # http://127.0.0.1:8475, blind
//	qpc-lab run -experiment qpc/experiments/round1.yaml
//	qpc-lab report
//	qpc-lab compare -calibrate eval/qpc,eval/qpc2    # unlabelled: where two runs differ
//
// Labels and rules: qpc/LABELS.md.
package main

import (
	"fmt"
	"log"
	"os"
)

func main() {
	log.SetFlags(0)
	log.SetPrefix("qpc-lab: ")
	if len(os.Args) < 2 {
		usage()
	}
	cmds := map[string]func([]string) error{
		"sample":  cmdSample,
		"enrich":  cmdEnrich,
		"label":   cmdLabel,
		"notes":   cmdNotes,
		"run":     cmdRun,
		"report":  cmdReport,
		"compare": cmdCompare,
	}
	cmd, ok := cmds[os.Args[1]]
	if !ok {
		usage()
	}
	if err := cmd(os.Args[2:]); err != nil {
		log.Fatal(err)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: qpc-lab sample|enrich|label|notes|run|report|compare [flags]   (-h for flags)")
	os.Exit(2)
}
