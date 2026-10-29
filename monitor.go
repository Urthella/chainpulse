package main

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type Endpoint struct {
	Chain string `json:"chain"`
	Name  string `json:"name"`
	Kind  string `json:"kind"`
	URL   string `json:"url"`
}

// State is what the last probes saw for one endpoint.
type State struct {
	Endpoint
	Up        bool          `json:"up"`
	Height    uint64        `json:"height"`
	Behind    uint64        `json:"behind"` // vs the highest endpoint of the same chain
	Latency   time.Duration `json:"latency_ns"`
	HeadSince time.Time     `json:"head_since"` // when Height last went up; a stuck node stops moving
	LastProbe time.Time     `json:"last_probe"`
	Err       string        `json:"error,omitempty"`
	OK        uint64        `json:"probes_ok"`
	Failed    uint64        `json:"probes_failed"`
}

type Monitor struct {
	client  *http.Client
	timeout time.Duration
	now     func() time.Time

	mu     sync.Mutex
	states []*State
}

func NewMonitor(endpoints []Endpoint, timeout time.Duration) (*Monitor, error) {
	m := &Monitor{client: &http.Client{}, timeout: timeout, now: time.Now}
	for _, e := range endpoints {
		if _, ok := Kinds[e.Kind]; !ok {
			return nil, fmt.Errorf("%s/%s: unknown kind %q (known: %s)", e.Chain, e.Name, e.Kind, strings.Join(kindNames(), ", "))
		}
		m.states = append(m.states, &State{Endpoint: e})
	}
	return m, nil
}

// ProbeAll probes every endpoint at once and waits for all of them.
func (m *Monitor) ProbeAll(ctx context.Context) {
	var wg sync.WaitGroup
	for _, s := range m.states {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.probe(ctx, s)
		}()
	}
	wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	best := map[string]uint64{}
	for _, s := range m.states {
		if s.Up && s.Height > best[s.Chain] {
			best[s.Chain] = s.Height
		}
	}
	for _, s := range m.states {
		s.Behind = 0
		if s.Up {
			s.Behind = best[s.Chain] - s.Height
		}
	}
}

func (m *Monitor) probe(ctx context.Context, s *State) {
	ctx, cancel := context.WithTimeout(ctx, m.timeout)
	defer cancel()
	start := m.now()
	height, err := Kinds[s.Kind].Probe(ctx, m.client, s.URL)
	elapsed := m.now().Sub(start)

	m.mu.Lock()
	defer m.mu.Unlock()
	s.LastProbe, s.Latency = start, elapsed
	if err != nil {
		s.Up, s.Err = false, err.Error()
		s.Failed++
		return
	}
	if height > s.Height || s.HeadSince.IsZero() {
		s.HeadSince = start
	}
	s.Up, s.Err, s.Height = true, "", height
	s.OK++
}

// Run probes on every tick until ctx is done.
func (m *Monitor) Run(ctx context.Context, every time.Duration) {
	m.ProbeAll(ctx)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ProbeAll(ctx)
		}
	}
}

// Snapshot copies the states, sorted by chain and endpoint name.
func (m *Monitor) Snapshot() []State {
	m.mu.Lock()
	out := make([]State, len(m.states))
	for i, s := range m.states {
		out[i] = *s
	}
	m.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Chain != out[j].Chain {
			return out[i].Chain < out[j].Chain
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Metrics renders the Prometheus text format.
func (m *Monitor) Metrics() string {
	now := m.now()
	var b strings.Builder
	series := []struct {
		name, help, kind string
		value            func(State) (float64, bool)
	}{
		{"chainpulse_up", "1 if the last probe returned a height.", "gauge", func(s State) (float64, bool) { return b2f(s.Up), true }},
		{"chainpulse_height", "Latest height reported (block, slot, checkpoint or masterchain seqno).", "gauge", func(s State) (float64, bool) { return float64(s.Height), s.OK > 0 }},
		{"chainpulse_behind", "How far behind the highest endpoint of the same chain.", "gauge", func(s State) (float64, bool) { return float64(s.Behind), s.Up }},
		{"chainpulse_head_age_seconds", "Seconds since the height last went up; a stuck node keeps growing this.", "gauge", func(s State) (float64, bool) { return now.Sub(s.HeadSince).Seconds(), s.OK > 0 }},
		{"chainpulse_latency_seconds", "Duration of the last probe.", "gauge", func(s State) (float64, bool) { return s.Latency.Seconds(), !s.LastProbe.IsZero() }},
		{"chainpulse_probes_failed_total", "Probes that returned no height.", "counter", func(s State) (float64, bool) { return float64(s.Failed), true }},
	}
	states := m.Snapshot()
	for _, se := range series {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", se.name, se.help, se.name, se.kind)
		for _, s := range states {
			if v, ok := se.value(s); ok {
				fmt.Fprintf(&b, "%s{chain=\"%s\",endpoint=\"%s\",kind=\"%s\"} %g\n", se.name, esc(s.Chain), esc(s.Name), esc(s.Kind), v)
			}
		}
	}
	return b.String()
}

var esc = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

func kindNames() []string {
	names := make([]string, 0, len(Kinds))
	for k := range Kinds {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}
