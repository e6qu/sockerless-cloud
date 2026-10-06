package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: container-command hold|http|serve|relay-http|probe-http|log|log-until|print|resolve|sleep|stdin-echo")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "hold":
		for {
			time.Sleep(time.Hour)
		}
	case "http":
		if len(os.Args) < 4 || len(os.Args) > 5 {
			fmt.Fprintln(os.Stderr, "usage: container-command http PORT RESPONSE [START_DELAY_SECONDS]")
			os.Exit(2)
		}
		port, err := strconv.Atoi(os.Args[2])
		if err != nil || port < 1 || port > 65535 {
			fmt.Fprintf(os.Stderr, "invalid HTTP port %q\n", os.Args[2])
			os.Exit(2)
		}
		if len(os.Args) == 5 {
			seconds, err := strconv.Atoi(os.Args[4])
			if err != nil || seconds < 0 {
				fmt.Fprintf(os.Stderr, "invalid HTTP start delay %q\n", os.Args[4])
				os.Exit(2)
			}
			time.Sleep(time.Duration(seconds) * time.Second)
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, os.Args[3])
		})
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), handler); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "serve":
		// An HTTP server that answers every request with RESPONSE and writes
		// one access-log line per request to stdout.
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: container-command serve PORT RESPONSE")
			os.Exit(2)
		}
		port, err := strconv.Atoi(os.Args[2])
		if err != nil || port < 1 || port > 65535 {
			fmt.Fprintf(os.Stderr, "invalid HTTP port %q\n", os.Args[2])
			os.Exit(2)
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Printf("%s %s\n", r.Method, r.URL.Path)
			_, _ = io.WriteString(w, os.Args[3])
		})
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), handler); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "relay-http":
		// An HTTP server that, per request, sends PAYLOAD and CRLF to the TCP
		// ADDRESS and answers with the first line that comes back, or 502
		// when the exchange fails.
		if len(os.Args) != 5 {
			fmt.Fprintln(os.Stderr, "usage: container-command relay-http PORT ADDRESS PAYLOAD")
			os.Exit(2)
		}
		port, err := strconv.Atoi(os.Args[2])
		if err != nil || port < 1 || port > 65535 {
			fmt.Fprintf(os.Stderr, "invalid HTTP port %q\n", os.Args[2])
			os.Exit(2)
		}
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			conn, err := net.DialTimeout("tcp", os.Args[3], 2*time.Second)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			if _, err := io.WriteString(conn, os.Args[4]+"\r\n"); err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			line, err := bufio.NewReader(conn).ReadString('\n')
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadGateway)
				return
			}
			_, _ = io.WriteString(w, line)
		})
		if err := http.ListenAndServe(fmt.Sprintf(":%d", port), handler); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	case "probe-http":
		if len(os.Args) != 5 {
			fmt.Fprintln(os.Stderr, "usage: container-command probe-http URL EXPECTED_RESPONSE TIMEOUT_SECONDS")
			os.Exit(2)
		}
		timeoutSeconds, err := strconv.Atoi(os.Args[4])
		if err != nil || timeoutSeconds < 1 {
			fmt.Fprintf(os.Stderr, "invalid timeout seconds %q\n", os.Args[4])
			os.Exit(2)
		}
		client := &http.Client{Timeout: 500 * time.Millisecond}
		deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
		for {
			response, requestErr := client.Get(os.Args[2])
			if requestErr == nil {
				body, readErr := io.ReadAll(response.Body)
				_ = response.Body.Close()
				if readErr == nil && response.StatusCode == http.StatusOK && string(body) == os.Args[3] {
					fmt.Println(os.Args[3])
					return
				}
			}
			if time.Now().After(deadline) {
				fmt.Fprintf(os.Stderr, "probe %s did not return %q\n", os.Args[2], os.Args[3])
				os.Exit(1)
			}
			time.Sleep(100 * time.Millisecond)
		}
	case "log":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: container-command log MESSAGE [SECONDS]")
			os.Exit(2)
		}
		fmt.Println(os.Args[2])
		if len(os.Args) >= 4 {
			seconds, err := strconv.Atoi(os.Args[3])
			if err != nil || seconds < 0 {
				fmt.Fprintf(os.Stderr, "invalid sleep seconds %q\n", os.Args[3])
				os.Exit(2)
			}
			time.Sleep(time.Duration(seconds) * time.Second)
		}
	case "log-until":
		// Announce MESSAGE, then hold until PATH exists, so whoever started
		// the workload decides when it ends by creating PATH.
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: container-command log-until MESSAGE PATH")
			os.Exit(2)
		}
		fmt.Println(os.Args[2])
		for {
			if _, err := os.Stat(os.Args[3]); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	case "print":
		if len(os.Args) < 3 {
			fmt.Fprintln(os.Stderr, "usage: container-command print MESSAGE")
			os.Exit(2)
		}
		fmt.Print(os.Args[2])
	case "resolve":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: container-command resolve HOST TIMEOUT_SECONDS [MESSAGE] [HOLD_SECONDS]")
			os.Exit(2)
		}
		timeoutSeconds, err := strconv.Atoi(os.Args[3])
		if err != nil || timeoutSeconds < 0 {
			fmt.Fprintf(os.Stderr, "invalid timeout seconds %q\n", os.Args[3])
			os.Exit(2)
		}
		message := "resolved " + os.Args[2]
		if len(os.Args) >= 5 {
			message = os.Args[4]
		}
		deadline := time.Now().Add(time.Duration(timeoutSeconds) * time.Second)
		for {
			addrs, err := net.LookupHost(os.Args[2])
			if err == nil && len(addrs) > 0 {
				fmt.Println(message)
				if len(os.Args) >= 6 {
					holdSeconds, err := strconv.Atoi(os.Args[5])
					if err != nil || holdSeconds < 0 {
						fmt.Fprintf(os.Stderr, "invalid hold seconds %q\n", os.Args[5])
						os.Exit(2)
					}
					time.Sleep(time.Duration(holdSeconds) * time.Second)
				}
				return
			}
			if time.Now().After(deadline) {
				fmt.Fprintf(os.Stderr, "resolve %s timed out\n", os.Args[2])
				os.Exit(1)
			}
			time.Sleep(100 * time.Millisecond)
		}
	case "sleep":
		if len(os.Args) != 3 {
			fmt.Fprintln(os.Stderr, "usage: container-command sleep SECONDS")
			os.Exit(2)
		}
		seconds, err := strconv.Atoi(os.Args[2])
		if err != nil || seconds < 0 {
			fmt.Fprintf(os.Stderr, "invalid sleep seconds %q\n", os.Args[2])
			os.Exit(2)
		}
		time.Sleep(time.Duration(seconds) * time.Second)
	case "stdin-echo":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}
