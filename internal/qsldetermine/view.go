package qsldetermine

// Status says whether and how a paper card can go out: "paper" (a usable
// route), "no-paper" (refused), "unclear" (wanted, but no usable route: a
// manager without a route, direct without a full address) or "unknown"
// (nothing stated). The vocabulary of qpc/LABELS.md.
func (r Result) Status() string {
	switch {
	case r.RefusePaper:
		return "no-paper"
	case r.Bureau || r.Direct || r.OQRS:
		return "paper"
	case r.Method == "M" || r.Unclear:
		return "unclear"
	}
	return "unknown"
}

// Routes lists the accepted routes in a stable order: bureau, direct, oqrs.
func (r Result) Routes() []string {
	if r.RefusePaper {
		return nil
	}
	var out []string
	if r.Bureau {
		out = append(out, "bureau")
	}
	if r.Direct {
		out = append(out, "direct")
	}
	if r.OQRS {
		out = append(out, "oqrs")
	}
	return out
}

// Suggest is the route the Desk preselects: "B" or "D" (the stated preferred
// route, else the cheaper one), "M" via a manager, "N" no card, or "" when
// nothing can be preselected (unknown, unclear, OQRS only).
func (r Result) Suggest() string {
	switch {
	case r.RefusePaper:
		return "N"
	case r.Method == "M":
		return "M"
	case r.Preferred == "B" && r.Bureau, r.Preferred == "D" && r.Direct:
		return r.Preferred
	case r.Bureau:
		return "B"
	case r.Direct:
		return "D"
	}
	return ""
}

// ManagerVia is how a card goes to the manager when Suggest is "M": "B" when
// only the bureau is named for it, "D" when direct is, "" when no route is
// named (the Desk then offers via manager, direct).
func (r Result) ManagerVia() string {
	if r.Method != "M" {
		return ""
	}
	switch {
	case r.Bureau && !r.Direct:
		return "B"
	case r.Direct:
		return "D"
	}
	return ""
}
