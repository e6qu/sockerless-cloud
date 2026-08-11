package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: http-localhost-probe server|probe|probe-once [MESSAGE]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "server":
		http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		})
		if err := http.ListenAndServe(":9090", nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "probe":
		http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			client := &http.Client{Timeout: 500 * time.Millisecond}
			resp, err := client.Get("http://127.0.0.1:9090/")
			if err != nil {
				w.Header().Set("X-Sockerless-Exit-Code", "1")
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, "sidecar-missing")
				return
			}
			_ = resp.Body.Close()
			w.Header().Set("X-Sockerless-Exit-Code", "0")
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "cloudrun-sidecar-ok")
		})
		if err := http.ListenAndServe(":8080", nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "probe-retry":
		// Long-lived HTTP server on :8080 that, on each request, retries
		// connecting to the sidecar on localhost:9090 for up to 10s before
		// answering — tolerates a sidecar still coming up when the first
		// invocation arrives (App Service / Cloud Run sidecar startup).
		message := "sidecar-ok"
		if len(os.Args) >= 3 {
			message = os.Args[2]
		}
		http.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
			client := &http.Client{Timeout: 500 * time.Millisecond}
			deadline := time.Now().Add(10 * time.Second)
			for {
				resp, err := client.Get("http://127.0.0.1:9090/")
				if err == nil {
					_ = resp.Body.Close()
					w.Header().Set("X-Sockerless-Exit-Code", "0")
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, message)
					return
				}
				if time.Now().After(deadline) {
					w.Header().Set("X-Sockerless-Exit-Code", "1")
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, "sidecar-missing")
					return
				}
				time.Sleep(100 * time.Millisecond)
			}
		})
		if err := http.ListenAndServe(":8080", nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "echo-request":
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, "%s %s", r.Method, r.URL.RequestURI())
		})
		if err := http.ListenAndServe(":8080", nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "probe-once":
		message := "cloudrun-job-sidecar-ok"
		if len(os.Args) >= 3 {
			message = os.Args[2]
		}
		client := &http.Client{Timeout: 500 * time.Millisecond}
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := client.Get("http://127.0.0.1:9090/")
			if err == nil {
				_ = resp.Body.Close()
				fmt.Println(message)
				return
			}
			if time.Now().After(deadline) {
				fmt.Fprintln(os.Stderr, "sidecar-missing")
				os.Exit(1)
			}
			time.Sleep(100 * time.Millisecond)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", os.Args[1])
		os.Exit(2)
	}
}
