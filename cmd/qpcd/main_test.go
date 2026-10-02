package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dl9et/qslotter/qpc"
)

func TestHandler(t *testing.T) {
	llm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"evidence\":\"buro\",\"status\":\"paper\",\"routes\":[\"bureau\"],\"preferred\":\"\",\"via\":\"\",\"note\":\"\",\"confidence\":\"high\"}"}}]}`)
	}))
	defer llm.Close()
	c, err := qpc.New(qpc.Variant{BaseURL: llm.URL, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	h := newHandler(c)

	post := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/classify", strings.NewReader(body)))
		return rec
	}
	rec := post(`{"station":{"call":"dl1abc","bio":"QSL via buro"}}`)
	var res qpc.Result
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != http.StatusOK || res.Status != qpc.Paper || len(res.Routes) != 1 || res.Routes[0] != qpc.Bureau || res.Call != "DL1ABC" {
		t.Errorf("classify: %d %s", rec.Code, rec.Body)
	}
	if rec := post(`{"station":{}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("missing call: %d", rec.Code)
	}
	if rec := post(`not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad body: %d", rec.Code)
	}

	llm.Close() // the model is gone
	if rec := post(`{"station":{"call":"K1A"}}`); rec.Code != http.StatusBadGateway {
		t.Errorf("unreachable model: %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"prompt":"v3@`) {
		t.Errorf("healthz: %d %s", rec.Code, rec.Body)
	}
}
