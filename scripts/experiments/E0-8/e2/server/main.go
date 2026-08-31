// E0-8 (a): a local HTTP server that serves one asset at an EXACT byte rate.
//
// The throttle is a paced writer, not a sleep-per-chunk approximation: after
// every chunk it computes the wall-clock instant at which that many bytes were
// "due" at the configured rate and sleeps until then, so drift never
// accumulates and the measured wall time is the honest one for the rate.
// It also flushes each chunk to the socket, so the client sees a genuine slow
// link rather than one big buffered burst at the end.
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:0", "listen address")
	file := flag.String("file", "", "asset to serve")
	rate := flag.Int64("rate", 0, "bytes per second (0 = unthrottled)")
	chunk := flag.Int("chunk", 16384, "write chunk size in bytes")
	portFile := flag.String("portfile", "", "write the chosen addr here")
	flag.Parse()

	body, err := os.ReadFile(*file)
	if err != nil {
		log.Fatalf("read asset: %v", err)
	}
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	if *portFile != "" {
		_ = os.WriteFile(*portFile, []byte(ln.Addr().String()), 0o644)
	}
	fmt.Printf("listening on %s serving %d bytes at rate=%d chunk=%d\n",
		ln.Addr().String(), len(body), *rate, *chunk)

	mux := http.NewServeMux()
	mux.HandleFunc("/asset", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		sent := 0
		for sent < len(body) {
			end := sent + *chunk
			if end > len(body) {
				end = len(body)
			}
			n, err := w.Write(body[sent:end])
			if err != nil {
				log.Printf("write error after %d bytes: %v", sent, err)
				return
			}
			sent += n
			if fl != nil {
				fl.Flush()
			}
			if *rate > 0 {
				due := start.Add(time.Duration(float64(sent) / float64(*rate) * float64(time.Second)))
				if d := time.Until(due); d > 0 {
					time.Sleep(d)
				}
			}
		}
		el := time.Since(start)
		log.Printf("served %d bytes in %.3fs (%.0f B/s effective)", sent, el.Seconds(),
			float64(sent)/el.Seconds())
	})
	mux.HandleFunc("/ping", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "ok") })
	log.Fatal(http.Serve(ln, mux))
}
