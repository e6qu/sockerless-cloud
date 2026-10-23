// Command metadata-client is a Cloud Run workload that reads the metadata
// server the way a Google client inside the container does, and answers each
// request with what it read:
//
//	/client/project-id, /client/numeric-project-id, /client/email?account=A
//	    the official metadata client's typed reads
//	/client/get?path=P    the official client's generic read of P
//	/client/subscribe/start?path=P
//	                      starts the official client's Subscribe on P and
//	                      answers the first value it sees
//	/client/subscribe/next
//	                      the next value that Subscribe sees, "<deleted>" for
//	                      a deletion
//	/adc                  an access token Application Default Credentials mint
//	/raw?path=P           a plain GET of /computeMetadata/v1/P, sending
//	                      Metadata-Flavor only when the request names one in
//	                      X-Metadata-Flavor, answered with the server's status,
//	                      body, and Metadata-Flavor and ETag headers as
//	                      X-Metadata-Flavor and X-Metadata-ETag
//	/root                 the same GET of the server's root, the residency
//	                      probe Google auth libraries send
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"

	"cloud.google.com/go/compute/metadata"
	"golang.org/x/oauth2/google"
)

func main() {
	client := metadata.NewClient(nil)
	answer := func(w http.ResponseWriter, value string, err error) {
		var undefined metadata.NotDefinedError
		switch {
		case errors.As(err, &undefined):
			http.Error(w, err.Error(), http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusBadGateway)
		default:
			_, _ = io.WriteString(w, value)
		}
	}
	http.HandleFunc("/client/project-id", func(w http.ResponseWriter, r *http.Request) {
		v, err := client.ProjectIDWithContext(r.Context())
		answer(w, v, err)
	})
	http.HandleFunc("/client/numeric-project-id", func(w http.ResponseWriter, r *http.Request) {
		v, err := client.NumericProjectIDWithContext(r.Context())
		answer(w, v, err)
	})
	http.HandleFunc("/client/email", func(w http.ResponseWriter, r *http.Request) {
		v, err := client.EmailWithContext(r.Context(), r.URL.Query().Get("account"))
		answer(w, v, err)
	})
	http.HandleFunc("/client/get", func(w http.ResponseWriter, r *http.Request) {
		v, err := client.GetWithContext(r.Context(), r.URL.Query().Get("path"))
		answer(w, v, err)
	})
	var subscription struct {
		sync.Mutex
		values chan string
	}
	next := func(w http.ResponseWriter, r *http.Request) {
		subscription.Lock()
		values := subscription.values
		subscription.Unlock()
		if values == nil {
			http.Error(w, "no subscription", http.StatusConflict)
			return
		}
		select {
		case v := <-values:
			answer(w, v, nil)
		case <-r.Context().Done():
		}
	}
	http.HandleFunc("/client/subscribe/start", func(w http.ResponseWriter, r *http.Request) {
		values := make(chan string, 16)
		subscription.Lock()
		subscription.values = values
		subscription.Unlock()
		go func() {
			err := client.SubscribeWithContext(context.Background(), r.URL.Query().Get("path"), func(_ context.Context, v string, ok bool) error {
				if !ok {
					v = "<deleted>"
				}
				values <- v
				return nil
			})
			fmt.Fprintln(os.Stderr, "subscribe:", err)
		}()
		next(w, r)
	})
	http.HandleFunc("/client/subscribe/next", next)
	http.HandleFunc("/adc", func(w http.ResponseWriter, r *http.Request) {
		creds, err := google.FindDefaultCredentials(r.Context(), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			answer(w, "", err)
			return
		}
		token, err := creds.TokenSource.Token()
		if err != nil {
			answer(w, "", err)
			return
		}
		answer(w, token.AccessToken, nil)
	})
	raw := func(w http.ResponseWriter, r *http.Request, path string) {
		req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
			"http://"+os.Getenv("GCE_METADATA_HOST")+path, nil)
		if err != nil {
			answer(w, "", err)
			return
		}
		if flavor := r.Header.Get("X-Metadata-Flavor"); flavor != "" {
			req.Header.Set("Metadata-Flavor", flavor)
		}
		resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
		if err != nil {
			answer(w, "", err)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("X-Metadata-Flavor", resp.Header.Get("Metadata-Flavor"))
		w.Header().Set("X-Metadata-ETag", resp.Header.Get("ETag"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}
	http.HandleFunc("/raw", func(w http.ResponseWriter, r *http.Request) {
		raw(w, r, "/computeMetadata/v1/"+r.URL.Query().Get("path"))
	})
	http.HandleFunc("/root", func(w http.ResponseWriter, r *http.Request) {
		raw(w, r, "/")
	})
	if err := http.ListenAndServe(":8080", nil); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
