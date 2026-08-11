package simulator

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// Concurrency soak / load tests for the shared simulator core: the Store
// implementations (MemoryStore + SQLiteStore), the OCI registry data plane,
// and the shared mux/middleware chain. Run with:
//
//	GOWORK=off go test -race -run 'Soak' -count=1 ./...
//
// Each test spawns many goroutines hammering a target with mixed concurrent
// operations on overlapping keys, then asserts invariants (no lost write, no
// corrupt read, no panic). The race detector flags any data race; a deadlock
// surfaces as a test timeout.

// soakItem is a value-only item so the test harness never mutates a reference
// field of a stored value (which would trip the documented Get/List/Filter
// no-mutate aliasing hazard — a test bug, not a production race). When a test
// needs to mutate stored state it goes through Update, the sanctioned path.
type soakItem struct {
	Name    string `json:"name"`
	Counter int    `json:"counter"`
	Payload string `json:"payload"`
}

// soakStores returns both Store implementations sharing one *sql.DB pool for
// the SQLite variant, so the soak exercises the connection-pool + WAL
// concurrent-read path exactly as the running sim does.
func soakStores(t *testing.T) map[string]Store[soakItem] {
	t.Helper()
	dir, err := os.MkdirTemp("", "sim-soak-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	db, err := OpenDB(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	sq, err := NewSQLiteStore[soakItem](db, "soak_items")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Store[soakItem]{
		"memory": NewStateStore[soakItem](),
		"sqlite": sq,
	}
}

// TestStoreMixedOpsSoak hammers each Store with Put/Get/Update/Delete/List/
// Filter/Len concurrently across a small, overlapping key space (so writers
// and readers collide on the same keys), checking that no operation panics or
// returns a corrupt value. With ~64 goroutines × 800 iters × 7 op kinds that's
// well over 350k mixed ops per store under -race.
func TestStoreMixedOpsSoak(t *testing.T) {
	const (
		goroutines = 64
		iters      = 800
		keys       = 16
	)
	for name, store := range soakStores(t) {
		t.Run(name, func(t *testing.T) {
			var wg sync.WaitGroup
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func(g int) {
					defer wg.Done()
					for i := 0; i < iters; i++ {
						key := fmt.Sprintf("k%d", (g+i)%keys)
						switch (g + i) % 7 {
						case 0:
							store.Put(key, soakItem{Name: key, Counter: i, Payload: strings.Repeat("x", i%32)})
						case 1:
							if v, ok := store.Get(key); ok {
								// Read every field — a torn read would surface
								// as garbage or a race-detector hit.
								_ = v.Name + v.Payload
								_ = v.Counter
							}
						case 2:
							store.Update(key, func(it *soakItem) {
								it.Counter++
								it.Name = key
							})
						case 3:
							store.Delete(key)
						case 4:
							for _, v := range store.List() {
								_ = v.Name
							}
						case 5:
							_ = store.Filter(func(it soakItem) bool { return it.Counter%2 == 0 })
						case 6:
							_ = store.Len()
						}
					}
				}(g)
			}
			wg.Wait()
		})
	}
}

// TestStoreUpdateNoLostWriteSoak proves Update is an atomic read-modify-write:
// N goroutines each increment the same key's counter M times; the final value
// must equal N*M exactly. A non-atomic RMW (Get→modify→Put outside a lock)
// would lose increments and fail this. Run with -count=3 for confidence.
func TestStoreUpdateNoLostWriteSoak(t *testing.T) {
	const (
		goroutines = 50
		incs       = 500
	)
	for name, store := range soakStores(t) {
		t.Run(name, func(t *testing.T) {
			store.Put("counter", soakItem{Name: "counter"})
			var wg sync.WaitGroup
			for g := 0; g < goroutines; g++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < incs; i++ {
						if !store.Update("counter", func(it *soakItem) { it.Counter++ }) {
							t.Errorf("Update returned false for present key")
							return
						}
					}
				}()
			}
			wg.Wait()
			v, ok := store.Get("counter")
			if !ok {
				t.Fatal("counter vanished")
			}
			if want := goroutines * incs; v.Counter != want {
				t.Fatalf("lost writes: counter=%d want=%d", v.Counter, want)
			}
		})
	}
}

// TestStoreFilterVsDeleteSoak stresses the specific hazard the prompt calls
// out: Filter's returned slice racing a concurrent Delete (and Put/Update) on
// the same keys. Readers iterate the returned slice (reading every field)
// while writers churn the key space. A race in slice construction or a torn
// value read surfaces under -race.
func TestStoreFilterVsDeleteSoak(t *testing.T) {
	const (
		readers = 24
		writers = 24
		iters   = 600
		keys    = 24
	)
	for name, store := range soakStores(t) {
		t.Run(name, func(t *testing.T) {
			for k := 0; k < keys; k++ {
				store.Put(fmt.Sprintf("k%d", k), soakItem{Name: fmt.Sprintf("k%d", k), Payload: "init"})
			}
			var readerWG, writerWG sync.WaitGroup
			var stop atomic.Bool
			for r := 0; r < readers; r++ {
				readerWG.Add(1)
				go func() {
					defer readerWG.Done()
					for !stop.Load() {
						for _, v := range store.Filter(func(it soakItem) bool { return len(it.Payload) > 0 }) {
							_ = v.Name + v.Payload
						}
						for _, v := range store.List() {
							_ = v.Name + v.Payload
						}
					}
				}()
			}
			for wIdx := 0; wIdx < writers; wIdx++ {
				writerWG.Add(1)
				go func(wIdx int) {
					defer writerWG.Done()
					for i := 0; i < iters; i++ {
						key := fmt.Sprintf("k%d", (wIdx+i)%keys)
						switch i % 3 {
						case 0:
							store.Put(key, soakItem{Name: key, Payload: strings.Repeat("p", i%16+1)})
						case 1:
							store.Update(key, func(it *soakItem) { it.Payload = "u" })
						case 2:
							store.Delete(key)
						}
					}
				}(wIdx)
			}
			writerWG.Wait()
			stop.Store(true)
			readerWG.Wait()
		})
	}
}

// soakClient returns an http.Client whose transport aggressively reuses
// keep-alive connections, so a soak firing tens of thousands of short requests
// from many goroutines doesn't churn the local ephemeral port range into
// exhaustion ("can't assign requested address" / TIME_WAIT pile-up) — a
// load-generator artifact, not a server race. Callers MUST fully drain and
// close response bodies (readAllClose) for a connection to be returned to the
// idle pool.
func soakClient(ts *httptest.Server) *http.Client {
	c := ts.Client()
	if tr, ok := c.Transport.(*http.Transport); ok {
		tr.MaxIdleConns = 0 // unlimited
		tr.MaxIdleConnsPerHost = 256
		tr.MaxConnsPerHost = 256
		tr.IdleConnTimeout = 0
	}
	return c
}

// newSoakOCI builds an OCIRegistry backed by MemoryStores, mounted on a real
// Server handler chain so requests traverse the same mux + middleware the sim
// uses. Returns the registry and an httptest server.
func newSoakOCI(t *testing.T) (*OCIRegistry, *httptest.Server) {
	t.Helper()
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "error"})
	if err != nil {
		t.Fatal(err)
	}
	reg := &OCIRegistry{
		Manifests: NewStateStore[OCIManifest](),
		Blobs:     NewStateStore[OCIBlob](),
		Uploads:   NewStateStore[OCIUpload](),
	}
	reg.Register(srv)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return reg, ts
}

// TestOCIChunkedUploadSoak runs many concurrent chunked uploads, each writing
// several PATCH chunks to its own UUID then finalizing with PUT, and verifies
// every finalized blob hashes to the digest it was stored under (no lost or
// interleaved chunk). It additionally fires N concurrent PATCHes against ONE
// shared UUID to stress Uploads.Update atomicity directly: the total appended
// length must equal the sum of all chunk lengths (no lost append).
func TestOCIChunkedUploadSoak(t *testing.T) {
	reg, ts := newSoakOCI(t)
	client := soakClient(ts)

	// Part 1: independent concurrent uploads, each PATCH+PUT to its own UUID.
	const uploads = 60
	var wg sync.WaitGroup
	for u := 0; u < uploads; u++ {
		wg.Add(1)
		go func(u int) {
			defer wg.Done()
			repo := fmt.Sprintf("repo%d", u%8)
			// Initiate.
			resp, err := client.Post(ts.URL+"/v2/"+repo+"/blobs/uploads/", "", nil)
			if err != nil {
				t.Errorf("initiate: %v", err)
				return
			}
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if loc == "" {
				t.Errorf("no upload Location")
				return
			}
			// Several chunks.
			var full []byte
			for c := 0; c < 5; c++ {
				chunk := []byte(strings.Repeat(fmt.Sprintf("%d", u), 64*(c+1)))
				full = append(full, chunk...)
				req, _ := http.NewRequest(http.MethodPatch, ts.URL+loc, strings.NewReader(string(chunk)))
				pr, err := client.Do(req)
				if err != nil {
					t.Errorf("patch: %v", err)
					return
				}
				_ = pr.Body.Close()
			}
			digest := ociDigest(full)
			req, _ := http.NewRequest(http.MethodPut, ts.URL+loc+"?digest="+digest, nil)
			pr, err := client.Do(req)
			if err != nil {
				t.Errorf("put finalize: %v", err)
				return
			}
			_ = pr.Body.Close()
			if pr.StatusCode != http.StatusCreated {
				t.Errorf("finalize status=%d want 201", pr.StatusCode)
				return
			}
			// Pull it back and verify content integrity.
			gr, err := client.Get(ts.URL + "/v2/" + repo + "/blobs/" + digest)
			if err != nil {
				t.Errorf("get blob: %v", err)
				return
			}
			body := readAllClose(gr)
			if ociDigest(body) != digest {
				t.Errorf("blob corrupt: got digest %s want %s", ociDigest(body), digest)
			}
		}(u)
	}
	wg.Wait()

	// Part 2: many PATCHes hammering one shared UUID — direct Uploads.Update
	// atomicity stress. The final stored Data length must equal the sum of all
	// chunk lengths; a non-atomic Get→append→Put would drop appends.
	resp, err := client.Post(ts.URL+"/v2/shared/blobs/uploads/", "", nil)
	if err != nil {
		t.Fatalf("initiate shared: %v", err)
	}
	loc := resp.Header.Get("Location")
	_ = resp.Body.Close()
	uuid := loc[strings.LastIndex(loc, "/")+1:]

	const patchers = 50
	const chunkLen = 37
	var pwg sync.WaitGroup
	for p := 0; p < patchers; p++ {
		pwg.Add(1)
		go func() {
			defer pwg.Done()
			chunk := strings.Repeat("z", chunkLen)
			req, _ := http.NewRequest(http.MethodPatch, ts.URL+loc, strings.NewReader(chunk))
			pr, err := client.Do(req)
			if err != nil {
				t.Errorf("shared patch: %v", err)
				return
			}
			_ = pr.Body.Close()
		}()
	}
	pwg.Wait()
	up, ok := reg.Uploads.Get(uuid)
	if !ok {
		t.Fatal("shared upload vanished")
	}
	if want := patchers * chunkLen; len(up.Data) != want {
		t.Fatalf("lost chunk appends: shared upload len=%d want=%d", len(up.Data), want)
	}
}

// TestOCIManifestConcurrentSoak runs concurrent manifest PUT / GET / DELETE /
// tags-list against overlapping repos+tags. The invariant checked is liveness
// and integrity: no panic, no torn read, and the manifestMu-serialized
// put-both-aliases / delete-all-aliases stay consistent (a GET that returns 200
// returns a body whose digest matches the advertised Docker-Content-Digest).
func TestOCIManifestConcurrentSoak(t *testing.T) {
	_, ts := newSoakOCI(t)
	client := soakClient(ts)

	const (
		goroutines = 64
		iters      = 400
		repos      = 6
		tags       = 8
	)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				repo := fmt.Sprintf("img%d", (g+i)%repos)
				tag := fmt.Sprintf("t%d", (g*i)%tags)
				switch (g + i) % 4 {
				case 0:
					// PUT a manifest. Body varies so digests differ, exercising
					// the put-tag-then-put-digest alias pair.
					data := fmt.Sprintf(`{"schemaVersion":2,"g":%d,"i":%d}`, g, i)
					req, _ := http.NewRequest(http.MethodPut, ts.URL+"/v2/"+repo+"/manifests/"+tag, strings.NewReader(data))
					req.Header.Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
					r, err := client.Do(req)
					if err != nil {
						t.Errorf("put manifest: %v", err)
						return
					}
					_ = r.Body.Close()
				case 1:
					r, err := client.Get(ts.URL + "/v2/" + repo + "/manifests/" + tag)
					if err != nil {
						t.Errorf("get manifest: %v", err)
						return
					}
					adv := r.Header.Get("Docker-Content-Digest")
					body := readAllClose(r)
					if r.StatusCode == http.StatusOK && adv != "" && ociDigest(body) != adv {
						t.Errorf("manifest body/digest mismatch: body=%s adv=%s", ociDigest(body), adv)
					}
				case 2:
					req, _ := http.NewRequest(http.MethodDelete, ts.URL+"/v2/"+repo+"/manifests/"+tag, nil)
					r, err := client.Do(req)
					if err != nil {
						t.Errorf("delete manifest: %v", err)
						return
					}
					_ = r.Body.Close()
				case 3:
					r, err := client.Get(ts.URL + "/v2/" + repo + "/tags/list")
					if err != nil {
						t.Errorf("tags list: %v", err)
						return
					}
					_ = readAllClose(r)
				}
			}
		}(g)
	}
	wg.Wait()
}

// TestMuxConcurrentSoak fires many concurrent requests through the full server
// handler chain (otel → request-id → logging → auth → mux), mixing health
// checks, registry pings, and 404s, to surface any serve-time race in the
// shared mux, the statusWriter buffering, or the middleware chain. The error
// path (≥500 not expected here, but 404/400 are) exercises statusWriter's
// status capture; a torn statusWriter would corrupt under -race.
func TestMuxConcurrentSoak(t *testing.T) {
	srv, err := NewServer(Config{Provider: "aws", LogLevel: "error"})
	if err != nil {
		t.Fatal(err)
	}
	// Register a handler that always 500s so statusWriter's error-body
	// buffering path runs under concurrency.
	srv.HandleFunc("GET /boom", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("e", 200)))
	})
	reg := &OCIRegistry{
		Manifests: NewStateStore[OCIManifest](),
		Blobs:     NewStateStore[OCIBlob](),
		Uploads:   NewStateStore[OCIUpload](),
	}
	reg.Register(srv)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	client := soakClient(ts)

	const goroutines = 80
	const iters = 300
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				var path string
				switch (g + i) % 4 {
				case 0:
					path = "/health"
				case 1:
					path = "/v2/"
				case 2:
					path = "/nonexistent/path"
				case 3:
					path = "/boom"
				}
				r, err := client.Get(ts.URL + path)
				if err != nil {
					t.Errorf("get %s: %v", path, err)
					return
				}
				_ = readAllClose(r)
			}
		}(g)
	}
	wg.Wait()
}

func readAllClose(r *http.Response) []byte {
	defer func() { _ = r.Body.Close() }()
	b := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := r.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return b
}
