package web

import (
	"sort"
	"strconv"
	"strings"

	"github.com/dl9et/qslotter/internal/i18n"
	"github.com/dl9et/qslotter/internal/qsldetermine"
	"github.com/dl9et/qslotter/internal/store"
)

// assessFor returns what QRZ states about QSL cards for a cached station
// (nil without station info). It is computed from the stored raw fields, so a
// change of the reading rules shows up at once; the result is memoised per
// (call, fetch time) because every row of every list asks for it.
func (s *Server) assessFor(info *store.StationInfo) *qsldetermine.Assessment {
	if info == nil || info.NotFound {
		return nil
	}
	key := strings.Join([]string{info.Callsign, info.FetchedAt, info.QSLMgr, info.MQSL, info.EQSL, info.LoTW, strconv.Itoa(len(info.BioText))}, "|")
	s.assessMu.Lock()
	defer s.assessMu.Unlock()
	if a, ok := s.assessMemo[key]; ok {
		return a
	}
	a := qsldetermine.Assess(qsldetermine.Input{Call: info.Callsign, QSLMgr: info.QSLMgr,
		MQSL: info.MQSL, EQSL: info.EQSL, LoTW: info.LoTW, Bio: info.BioText})
	if s.assessMemo == nil || len(s.assessMemo) > 2000 {
		s.assessMemo = map[string]*qsldetermine.Assessment{}
	}
	s.assessMemo[key] = &a
	return &a
}

// SigChip is one stated fact in the signal strip.
type SigChip struct {
	Kind     string // css class suffix
	Text     i18n.Msg
	Title    i18n.Msg // source and the quoted words
	Decisive bool     // the suggestion rests on it
	Scoped   bool     // limited to a case, never decisive
}

// SigView is what the research panel shows about a station's QSL wishes: the
// stated facts as chips, then either the suggestion with its reason or the
// reason there is none.
type SigView struct {
	Chips   []SigChip
	Suggest string   // "B", "D", "M", "N" or ""
	Manager string   // callsign for "M"
	Why     i18n.Msg // the words the suggestion rests on
	Note    i18n.Msg // why there is no suggestion
}

// sigViewFor renders an assessment (nil for no station info).
func sigViewFor(a *qsldetermine.Assessment) *SigView {
	if a == nil {
		return nil
	}
	v := &SigView{Suggest: a.Suggest, Manager: a.Manager}
	for _, sg := range a.Signals {
		if sg.Kind == qsldetermine.KindFlag {
			continue // the flags line shows them
		}
		c := SigChip{Kind: string(sg.Kind), Decisive: sg.Decisive, Scoped: sg.Scoped}
		c.Text = sigText(sg)
		src := i18n.M("bio")
		if sg.Source == qsldetermine.SourceQSLMgr {
			src = i18n.M("qslmgr field")
		}
		if sg.Scoped {
			c.Title = i18n.M("%s: \"%s\" - limited to a case, not used", src, sg.Quote)
		} else {
			c.Title = i18n.M("%s: \"%s\"", src, sg.Quote)
		}
		v.Chips = append(v.Chips, c)
	}
	sort.SliceStable(v.Chips, func(i, j int) bool { return chipRank[v.Chips[i].Kind] < chipRank[v.Chips[j].Kind] })

	switch {
	case a.Suggest != "" && a.Both:
		v.Why = i18n.M("bureau and direct both listed - bureau is cheaper")
	case a.Suggest != "":
		if d := a.Decisive(); len(d) > 0 {
			src := i18n.M("bio")
			if d[0].Source == qsldetermine.SourceQSLMgr {
				src = i18n.M("qslmgr field")
			}
			v.Why = i18n.M("%s: \"%s\"", src, d[0].Quote)
		}
	default:
		switch a.Note {
		case qsldetermine.NoteElectronicOnly:
			v.Note = i18n.M("Only electronic confirmations listed - no paper route stated.")
		case qsldetermine.NotePaperNoRoute:
			v.Note = i18n.M("Paper QSL accepted, no route stated.")
		case qsldetermine.NoteConflict:
			v.Note = i18n.M("qslmgr and bio contradict each other - nothing suggested.")
		case qsldetermine.NoteScopedRefusal:
			v.Note = i18n.M("A refusal limited to a case (see the chip) - nothing suggested.")
		default:
			v.Note = i18n.M("No QSL route found in QRZ's qslmgr or bio.")
		}
	}
	return v
}

var chipRank = map[string]int{
	"manager": 0, "bureau": 1, "only-bureau": 1, "direct": 2, "only-direct": 2, "no-bureau": 3, "no-direct": 3,
	"refuses-paper": 4, "accepts-paper": 5, "oqrs": 6, "electronic": 7,
}

func sigText(sg qsldetermine.Signal) i18n.Msg {
	switch sg.Kind {
	case qsldetermine.KindBureau:
		return i18n.M("Bureau")
	case qsldetermine.KindDirect:
		return i18n.M("Direct")
	case qsldetermine.KindOnlyBureau:
		return i18n.M("Bureau only")
	case qsldetermine.KindOnlyDirect:
		return i18n.M("Direct only")
	case qsldetermine.KindNoBureau:
		return i18n.M("No bureau")
	case qsldetermine.KindNoDirect:
		return i18n.M("No direct")
	case qsldetermine.KindManager:
		return i18n.M("Manager %s", sg.Value)
	case qsldetermine.KindRefusesPaper:
		if sg.Value == "electronic-only" {
			return i18n.M("Electronic only")
		}
		return i18n.M("No paper QSL")
	case qsldetermine.KindAcceptsPaper:
		return i18n.M("Paper QSL")
	case qsldetermine.KindOQRS:
		return i18n.M("OQRS")
	}
	return i18n.M("%s", strings.TrimSpace(sg.Value))
}

// routeFromAssessment is the route the Desk (and a reply to a received card)
// offers first when QRZ states one: B and D as stated, a manager as via
// manager - bureau when QRZ says so, else direct. Refusals and silence
// preselect nothing: the operator chooses.
func routeFromAssessment(row *QueueRow) (code, manager string) {
	manager = row.MgrPrefill
	switch row.Suggested {
	case "B", "D":
		return row.Suggested, manager
	case "M":
		if manager == "" {
			return "", manager
		}
		if row.MgrVia == "B" {
			return "MB", manager
		}
		return "MD", manager
	}
	return "", manager
}
