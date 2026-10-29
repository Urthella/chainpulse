// Command chainpulse watches RPC endpoints across many blockchains: is each one up, how high is
// its head, how far behind its peers is it, and has it stopped moving.
package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"text/tabwriter"
	"time"
)

const version = "0.1.0"

//go:embed endpoints.json
var defaultEndpoints []byte

func main() {
	config := flag.String("config", "", "endpoints JSON file (default: built-in public endpoints for 15 chains)")
	once := flag.Bool("once", false, "probe every endpoint once, print a table, exit 1 if any is down")
	listen := flag.String("listen", ":9090", "address for /metrics and /status")
	every := flag.Duration("interval", 30*time.Second, "time between probe rounds")
	timeout := flag.Duration("timeout", 10*time.Second, "per-probe timeout")
	flag.Parse()
	log.SetFlags(0)

	data := defaultEndpoints
	if *config != "" {
		var err error
		if data, err = os.ReadFile(*config); err != nil {
			log.Fatalf("chainpulse: %v", err)
		}
	}
	var endpoints []Endpoint
	if err := json.Unmarshal(data, &endpoints); err != nil {
		log.Fatalf("chainpulse: endpoints: %v", err)
	}
	m, err := NewMonitor(endpoints, *timeout)
	if err != nil {
		log.Fatalf("chainpulse: %v", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *once {
		m.ProbeAll(ctx)
		states := m.Snapshot()
		table(os.Stdout, states)
		for _, s := range states {
			if !s.Up {
				stop()
				os.Exit(1)
			}
		}
		return
	}

	go m.Run(ctx, *every)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = io.WriteString(w, m.Metrics())
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(m.Snapshot())
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	srv := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	log.Printf("chainpulse: watching %d endpoints every %s, metrics on %s/metrics", len(endpoints), *every, *listen)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("chainpulse: %v", err)
	}
}

func table(w io.Writer, states []State) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "CHAIN\tENDPOINT\tHEIGHT\tUNIT\tBEHIND\tLATENCY\tSTATUS")
	for _, s := range states {
		height, behind, status := "-", "-", "ok"
		if s.Up {
			height, behind = thousands(s.Height), strconv.FormatUint(s.Behind, 10)
		} else {
			status = "DOWN " + s.Err
			if len(status) > 70 {
				status = status[:70] + "…"
			}
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Chain, s.Name, height, Kinds[s.Kind].Unit, behind, s.Latency.Round(time.Millisecond), status)
	}
	_ = tw.Flush()
}

func thousands(n uint64) string {
	s := strconv.FormatUint(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
