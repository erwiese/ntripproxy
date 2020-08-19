package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/de-bkg/gognss/pkg/ntrip"
)

const (
	version = "0.1"

	// Attempts stores the numner of attempts for the same request
	Attempts int = iota
	Retry
)

var sourcetable *ntrip.Sourcetable

// Backend holds the data about a NtripCaster.
type Backend struct {
	*ntrip.Client // Caster client
	URL           *url.URL
	sourcetable   *ntrip.Sourcetable
	Alive         bool
	mux           sync.RWMutex
	ReverseProxy  *httputil.ReverseProxy
}

type backendOpts struct {
	mountpoint string
}

// SetAlive for this backend.
func (b *Backend) SetAlive(alive bool) {
	b.mux.Lock()
	b.Alive = alive
	b.mux.Unlock()
}

// IsAlive returns true when backend is alive
func (b *Backend) IsAlive() (alive bool) {
	b.mux.RLock()
	alive = b.Alive
	b.mux.RUnlock()
	return
}

// BackendPool holds information about reachable backends.
type BackendPool struct {
	backends []*Backend
	current  uint64
}

// AddBackend to the server pool
func (bp *BackendPool) AddBackend(backend *Backend) {
	bp.backends = append(bp.backends, backend)
}

// NextIndex atomically increase the counter and return an index
func (bp *BackendPool) NextIndex() int {
	return int(atomic.AddUint64(&bp.current, uint64(1)) % uint64(len(bp.backends)))
}

// MarkBackendStatus changes a status of a backend
func (bp *BackendPool) MarkBackendStatus(casterURL *url.URL, alive bool) {
	for _, b := range bp.backends {
		if b.URL.String() == casterURL.String() {
			b.SetAlive(alive)
			break
		}
	}
}

// GetNextPeer returns next active peer to take a connection
func (bp *BackendPool) GetNextPeer(opts backendOpts) *Backend {
	// loop entire backends to find out an Alive backend
	next := bp.NextIndex()
	l := len(bp.backends) + next // start from next and move a full cycle
	for i := next; i < l; i++ {
		idx := i % len(bp.backends)     // take an index by modding
		if bp.backends[idx].IsAlive() { // if we have an alive backend, use it and store if its not the original one
			if opts.mountpoint != "" {
				if _, found := bp.backends[idx].sourcetable.HasStream(opts.mountpoint); found == false {
					continue
				}
			}

			if i != next {
				atomic.StoreUint64(&bp.current, uint64(idx))
			}
			return bp.backends[idx]
		}
	}
	return nil
}

// HealthCheck pings the backends and update the status
func (bp *BackendPool) HealthCheck() {
	var err error
	stables := make([]*ntrip.Sourcetable, 0, len(bp.backends))
	for _, b := range bp.backends {
		status := "up"
		alive := true
		//alive := isCasterAlive(b.URL)
		//alive := b.IsCasterAlive()
		st, err := b.ParseSourcetable()
		if err != nil {
			status = "down"
			alive = false
		} else {
			stables = append(stables, st)
		}
		b.sourcetable = st
		b.SetAlive(alive)
		// if !alive {
		// 	status = "down"
		// }
		log.Printf("%s is %s\n", b.URL, status)
	}

	// Update combined sourcetable
	sourcetable, err = ntrip.MergeSourcetables(stables...)
	if err != nil {
		log.Printf("Could not build the combined sourcetable: %v\n", err)
	}

}

// GetAttemptsFromContext returns the attempts for request
func GetAttemptsFromContext(req *http.Request) int {
	if attempts, ok := req.Context().Value(Attempts).(int); ok {
		return attempts
	}
	return 1
}

// GetRetryFromContext returns the retries for request
func GetRetryFromContext(req *http.Request) int {
	if retry, ok := req.Context().Value(Retry).(int); ok {
		return retry
	}
	return 0
}

// lb load balances the incoming request
func lb(w http.ResponseWriter, req *http.Request) {
	attempts := GetAttemptsFromContext(req)
	if attempts > 3 {
		log.Printf("%s(%s) Max attempts reached, terminating\n", req.RemoteAddr, req.URL.Path)
		http.Error(w, "Service not available", http.StatusServiceUnavailable)
		return
	}

	path := req.URL.Path
	if path == "/" {
		log.Println("request combined sourcetable")
		// TODO: build common sourcetable

		// w.Header().Set("Trailer", "AtEnd1, AtEnd2")
		// w.Header().Add("Trailer", "AtEnd3")
		w.Header().Set("Ntrip-Version", "Ntrip/2.0")
		w.Header().Set("Server", "NTRIP BKG Proxy 0.1/2.0")
		w.Header().Set("Content-Type", "gnss/sourcetable")
		w.Header().Set("Connection", "close")
		//w.WriteHeader(http.StatusOK) // implicit
		err := sourcetable.Write(w)
		if err != nil {
			log.Printf("Could not write sourcetable: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		//io.WriteString(w, "Here comes the combined sourcetable...\n")
		return
	}

	log.Printf("request stream: %s", path)
	if strings.HasPrefix(path, "?") {
		http.Error(w, "Service not available", http.StatusServiceUnavailable)
		return
	}

	peer := backendPool.GetNextPeer(backendOpts{mountpoint: strings.TrimPrefix(path, "/")})
	if peer != nil {

		log.Printf("Forward req to %s", peer.URL.Hostname())

		// // Update the headers to allow for SSL redirection
		// req.URL.Host = url.Host
		// req.URL.Scheme = url.Scheme
		// req.Header.Set("X-Forwarded-Host", req.Header.Get("Host"))
		// req.Host = url.Host

		// Dump request
		dump, _ := httputil.DumpRequest(req, false)
		log.Printf("request: %q", dump)
		//fmt.Println(string(dump))

		peer.ReverseProxy.ServeHTTP(w, req)
		return
	}
	http.Error(w, "Service not available", http.StatusServiceUnavailable)
}

// isAlive checks whether a backend is Alive by establishing a TCP connection
/* func isCasterAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		log.Println("Backend unreachable, error: ", err)
		return false
	}
	defer conn.Close()
	return true
} */

// healthCheck runs a routine for check status of the backends every 2 mins
func healthCheck() {
	t := time.NewTicker(time.Second * 60)
	for {
		select {
		case <-t.C:
			log.Println("Starting health check...")
			backendPool.HealthCheck()
		}
	}
}

func logConnState(conn net.Conn, state http.ConnState) {
	log.Printf("client %v to remote %v conn state: %v", conn.LocalAddr(), conn.RemoteAddr(), state)
}

var backendPool BackendPool

func main() {
	var serverList string
	var port int
	fs := flag.NewFlagSet("ntripproxy/"+version, flag.ExitOnError)
	fs.StringVar(&serverList, "backends", "", "Load balanced backends, use comma to separate")
	fs.IntVar(&port, "port", 3030, "Port to serve")
	fs.Parse(os.Args[1:])

	if len(serverList) == 0 {
		log.Fatal("Please provide one or more backends to load balance")
	}

	// parse servers
	tokens := strings.Split(serverList, ",")
	for _, tok := range tokens {
		serverURL, err := url.Parse(tok)
		if err != nil {
			log.Fatal(err)
		}

		proxy := httputil.NewSingleHostReverseProxy(serverURL)
		proxy.ErrorHandler = func(writer http.ResponseWriter, req *http.Request, e error) {
			log.Printf("[%s] %s\n", serverURL.Host, e.Error())
			retries := GetRetryFromContext(req)
			if retries < 3 {
				select {
				case <-time.After(10 * time.Millisecond):
					ctx := context.WithValue(req.Context(), Retry, retries+1)
					proxy.ServeHTTP(writer, req.WithContext(ctx))
				}
				return
			}

			// after 3 retries, mark this backend as down
			backendPool.MarkBackendStatus(serverURL, false)

			// if the same request routing for few attempts with different backends, increase the count
			attempts := GetAttemptsFromContext(req)
			log.Printf("%s(%s) Attempting retry %d\n", req.RemoteAddr, req.URL.Path, attempts)
			ctx := context.WithValue(req.Context(), Attempts, attempts+1)
			lb(writer, req.WithContext(ctx))
		}

		cli, err := ntrip.NewClient(serverURL.String(), ntrip.Options{})
		if err != nil {
			log.Fatal(err)
		}

		backendPool.AddBackend(&Backend{
			Client:       cli,
			URL:          serverURL,
			Alive:        true,
			ReverseProxy: proxy,
		})

		log.Printf("Configured backend: %s\n", serverURL)

		// to get the combined sourcetable
		backendPool.HealthCheck()
	}

	// create http server
	server := http.Server{
		Addr:      fmt.Sprintf(":%d", port),
		Handler:   http.HandlerFunc(lb),
		ConnState: logConnState,
	}

	// start health checking
	go healthCheck()

	log.Printf("ntripproxy started at :%d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
