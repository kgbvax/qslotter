package qsldetermine

import (
	"fmt"
	"testing"
)

func TestResultView(t *testing.T) {
	for _, c := range []struct {
		r                   Result
		status, routes, sug string
		via                 string
	}{
		{Result{Method: "B", Bureau: true, Direct: true}, "paper", "[bureau direct]", "B", ""},
		{Result{Method: "B", Bureau: true, Direct: true, Preferred: "D"}, "paper", "[bureau direct]", "D", ""},
		{Result{Method: "D", Direct: true, OQRS: true}, "paper", "[direct oqrs]", "D", ""},
		{Result{OQRS: true}, "paper", "[oqrs]", "", ""},
		{Result{Method: "M", Manager: "EA5GL"}, "unclear", "[]", "M", ""},
		{Result{Method: "M", Manager: "K2ABC", Bureau: true}, "paper", "[bureau]", "M", "B"},
		{Result{Method: "M", Manager: "K2ABC", Direct: true}, "paper", "[direct]", "M", "D"},
		{Result{Unclear: true}, "unclear", "[]", "", ""},
		{Result{RefusePaper: true}, "no-paper", "[]", "N", ""},
		{Result{}, "unknown", "[]", "", ""},
	} {
		if s, rt, sg, v := c.r.Status(), fmt.Sprint(c.r.Routes()), c.r.Suggest(), c.r.ManagerVia(); s != c.status || rt != c.routes || sg != c.sug || v != c.via {
			t.Errorf("%+v: %s %s %s %s", c.r, s, rt, sg, v)
		}
	}
}
