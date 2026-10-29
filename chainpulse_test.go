package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Every fixture in testdata is a real response captured from a public mainnet endpoint.
func TestParsesRealResponsesFromNineProtocols(t *testing.T) {
	want := map[string]uint64{
		"evm":       0x1914606, // Ethereum
		"solana":    462851003,
		"starknet":  17122226,
		"near":      222113864,
		"cometbft":  33631278, // Cosmos Hub, height as a string
		"sui":       339388497,
		"aptos":     1146927145, // also a string
		"toncenter": 102066047,
		"esplora":   973682, // Bitcoin, plain text
	}
	for kind, height := range want {
		file := "testdata/" + kind + ".json"
		if kind == "esplora" {
			file = "testdata/esplora.txt"
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		got, err := parseHeight(data, Kinds[kind].Field)
		if err != nil || got != height {
			t.Errorf("%s: got %d, %v; want %d", kind, got, err, height)
		}
	}
	if len(want) != len(Kinds) {
		t.Errorf("%d kinds but %d fixtures", len(Kinds), len(want))
	}
}

func TestErrorsAreReportedNotParsedAsZero(t *testing.T) {
	deprecated, _ := os.ReadFile("testdata/sui-jsonrpc-deprecated.json") // what Sui's old JSON-RPC answers today
	cases := map[string]struct {
		body, field, want string
	}{
		"json-rpc error": {string(deprecated), "result", "JSON-RPC on public fullnodes has been deprecated"},
		"graphql error":  {`{"errors":[{"message":"rate limited"}]}`, "data.checkpoint.sequenceNumber", "rate limited"},
		"missing field":  {`{"result":{}}`, "result.header.height", "no result.header.height"},
		"not a number":   {`{"result":true}`, "result", "not a number"},
		"html page":      {`<html>502</html>`, "result", "not JSON"},
	}
	for name, c := range cases {
		if _, err := parseHeight([]byte(c.body), c.field); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want %q", name, err, c.want)
		}
	}
}

// fakeNode answers eth_blockNumber with a height the test controls, or fails when it's 0.
func fakeNode(t *testing.T, height *atomic.Uint64) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Method string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "eth_blockNumber" || r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		h := height.Load()
		if h == 0 {
			http.Error(w, "upstream down", http.StatusBadGateway)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 1, "result": "0x" + strconv.FormatUint(h, 16)})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestBehindHeadAgeAndDown(t *testing.T) {
	var fast, slow, dead atomic.Uint64
	fast.Store(1000)
	slow.Store(990)
	m, err := NewMonitor([]Endpoint{
		{Chain: "ethereum", Name: "fast", Kind: "evm", URL: fakeNode(t, &fast)},
		{Chain: "ethereum", Name: "slow", Kind: "evm", URL: fakeNode(t, &slow)},
		{Chain: "base", Name: "dead", Kind: "evm", URL: fakeNode(t, &dead)},
	}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_000_000, 0)
	m.now = func() time.Time { return clock }

	m.ProbeAll(context.Background())
	byName := func() map[string]State {
		out := map[string]State{}
		for _, s := range m.Snapshot() {
			out[s.Name] = s
		}
		return out
	}
	s := byName()
	if !s["fast"].Up || s["fast"].Behind != 0 || s["slow"].Behind != 10 {
		t.Fatalf("behind: fast %+v, slow %+v", s["fast"], s["slow"])
	}
	if s["dead"].Up || !strings.Contains(s["dead"].Err, "HTTP 502") || s["dead"].Failed != 1 {
		t.Fatalf("dead: %+v", s["dead"])
	}

	// A minute later fast moved on and slow is stuck: its head age keeps growing.
	clock = clock.Add(time.Minute)
	fast.Store(1005)
	m.ProbeAll(context.Background())
	s = byName()
	if s["slow"].Behind != 15 || !s["slow"].HeadSince.Equal(time.Unix(1_000_000, 0)) || !s["fast"].HeadSince.Equal(clock) {
		t.Fatalf("stuck node: slow %+v fast %+v", s["slow"], s["fast"])
	}
	metrics := m.Metrics()
	for _, want := range []string{
		`chainpulse_up{chain="base",endpoint="dead",kind="evm"} 0`,
		`chainpulse_behind{chain="ethereum",endpoint="slow",kind="evm"} 15`,
		`chainpulse_head_age_seconds{chain="ethereum",endpoint="slow",kind="evm"} 60`,
		`chainpulse_height{chain="ethereum",endpoint="fast",kind="evm"} 1005`,
	} {
		if !strings.Contains(metrics, want) {
			t.Errorf("missing %s in:\n%s", want, metrics)
		}
	}
	if strings.Contains(metrics, `chainpulse_height{chain="base"`) {
		t.Error("an endpoint that never answered must not report height 0")
	}
}

func TestTable(t *testing.T) {
	var buf bytes.Buffer
	table(&buf, []State{
		{Endpoint: Endpoint{Chain: "solana", Name: "official", Kind: "solana"}, Up: true, Height: 462851003, Latency: 337 * time.Millisecond},
		{Endpoint: Endpoint{Chain: "sui", Name: "jsonrpc", Kind: "sui"}, Err: "error: Method not found"},
	})
	out := buf.String()
	if !strings.Contains(out, "462,851,003") || !strings.Contains(out, "slot") || !strings.Contains(out, "DOWN error: Method not found") {
		t.Errorf("table:\n%s", out)
	}
}

func TestDefaultEndpointsAreValid(t *testing.T) {
	var endpoints []Endpoint
	if err := json.Unmarshal(defaultEndpoints, &endpoints); err != nil {
		t.Fatal(err)
	}
	if _, err := NewMonitor(endpoints, time.Second); err != nil {
		t.Fatal(err)
	}
	chains := map[string]bool{}
	for _, e := range endpoints {
		chains[e.Chain] = true
	}
	if len(chains) < 12 {
		t.Errorf("only %d chains in the default config", len(chains))
	}
	if _, err := NewMonitor([]Endpoint{{Chain: "x", Name: "y", Kind: "bitcoin-core"}}, time.Second); err == nil {
		t.Error("unknown kind accepted")
	}
}

// TestLive probes the real public endpoints; run it with CHAINPULSE_LIVE=1.
func TestLive(t *testing.T) {
	if os.Getenv("CHAINPULSE_LIVE") == "" {
		t.Skip("set CHAINPULSE_LIVE=1 to probe the public endpoints")
	}
	var endpoints []Endpoint
	_ = json.Unmarshal(defaultEndpoints, &endpoints)
	m, _ := NewMonitor(endpoints, 15*time.Second)
	m.ProbeAll(context.Background())
	var buf bytes.Buffer
	table(&buf, m.Snapshot())
	t.Log("\n" + buf.String())
	up := 0
	for _, s := range m.Snapshot() {
		if s.Up {
			up++
		}
	}
	if up < len(endpoints)*8/10 { // public endpoints flake; most should answer
		t.Errorf("only %d of %d endpoints up", up, len(endpoints))
	}
}
