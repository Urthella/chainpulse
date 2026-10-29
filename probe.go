package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// Kind is how to ask one family of nodes for its latest height. Fifteen chains speak nine
// protocols, and every one of them comes down to a request plus a dot path into the response.
type Kind struct {
	Method string // POST sends Body as JSON; GET sends nothing
	Path   string // appended to the endpoint URL
	Body   string
	Field  string // dot path to the height; "" means the whole body is the number
	Unit   string // what the height counts
}

var Kinds = map[string]Kind{
	"evm":       {Method: "POST", Body: rpc("eth_blockNumber", `[]`), Field: "result", Unit: "block"},
	"solana":    {Method: "POST", Body: rpc("getSlot", `[{"commitment":"confirmed"}]`), Field: "result", Unit: "slot"},
	"starknet":  {Method: "POST", Body: rpc("starknet_blockNumber", `[]`), Field: "result", Unit: "block"},
	"near":      {Method: "POST", Body: rpc("block", `{"finality":"final"}`), Field: "result.header.height", Unit: "block"},
	"cometbft":  {Method: "POST", Body: rpc("status", `{}`), Field: "result.sync_info.latest_block_height", Unit: "block"},
	"sui":       {Method: "POST", Body: `{"query":"{ checkpoint { sequenceNumber } }"}`, Field: "data.checkpoint.sequenceNumber", Unit: "checkpoint"}, // GraphQL: Sui retired JSON-RPC on public nodes
	"aptos":     {Method: "GET", Field: "block_height", Unit: "block"},
	"toncenter": {Method: "GET", Path: "/getMasterchainInfo", Field: "result.last.seqno", Unit: "masterchain block"},
	"esplora":   {Method: "GET", Path: "/blocks/tip/height", Unit: "block"},
}

func rpc(method, params string) string {
	return `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":` + params + `}`
}

// Probe asks one endpoint for its latest height.
func (k Kind) Probe(ctx context.Context, client *http.Client, url string) (uint64, error) {
	var body io.Reader
	if k.Method == http.MethodPost {
		body = strings.NewReader(k.Body)
	}
	req, err := http.NewRequestWithContext(ctx, k.Method, strings.TrimRight(url, "/")+k.Path, body)
	if err != nil {
		return 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "chainpulse/"+version)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(data))
	}
	return parseHeight(data, k.Field)
}

func parseHeight(data []byte, field string) (uint64, error) {
	if field == "" {
		return parseNumber(strings.TrimSpace(string(data)))
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber() // heights as exact strings, never float64
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return 0, fmt.Errorf("not JSON: %s", snippet(data))
	}
	if m, ok := doc.(map[string]any); ok {
		for _, key := range []string{"error", "errors"} { // JSON-RPC and GraphQL errors
			if e, ok := m[key]; ok && e != nil {
				b, _ := json.Marshal(e)
				return 0, fmt.Errorf("%s: %s", key, snippet(b))
			}
		}
	}
	v := doc
	for _, key := range strings.Split(field, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return 0, fmt.Errorf("response has no %s", field)
		}
		v = m[key]
	}
	switch x := v.(type) {
	case json.Number:
		return parseNumber(x.String())
	case string: // Aptos and CometBFT send heights as strings
		return parseNumber(x)
	case nil:
		return 0, fmt.Errorf("response has no %s", field)
	}
	return 0, fmt.Errorf("%s is not a number: %v", field, v)
}

// parseNumber reads decimal, or 0x-prefixed hex as EVM nodes send it.
func parseNumber(s string) (uint64, error) {
	if hex, ok := strings.CutPrefix(s, "0x"); ok {
		return strconv.ParseUint(hex, 16, 64)
	}
	return strconv.ParseUint(s, 10, 64)
}

func snippet(b []byte) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
