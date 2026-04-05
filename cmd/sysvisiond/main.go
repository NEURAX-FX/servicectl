package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"servicectl/internal/visionapi"
)

type subscriber struct {
	filter visionapi.WatchFilter
	ch     chan visionapi.EventEnvelope
}

type daemon struct {
	userMode                  bool
	runtime                   string
	logger                    *log.Logger
	mu                        sync.Mutex
	nextID                    int
	subs                      map[int]subscriber
	servicectlEventsConnected bool
	servicectlEventsError     string
}

type metaResponse struct {
	ServicectlEventsConnected bool   `json:"servicectl_events_connected"`
	ServicectlEventsError     string `json:"servicectl_events_error,omitempty"`
}

func main() {
	userMode := flag.Bool("user", false, "run in user mode")
	flag.Parse()
	logger := log.New(os.Stdout, "sysvisiond: ", log.LstdFlags)
	d := &daemon{userMode: *userMode, runtime: visionapi.RuntimeDir(*userMode, os.Getenv("XDG_RUNTIME_DIR")), logger: logger, subs: make(map[int]subscriber)}
	if err := d.run(); err != nil {
		logger.Fatal(err)
	}
}

func (d *daemon) run() error {
	if err := os.MkdirAll(visionapi.SysvisionDir(d.userMode, d.runtime), 0755); err != nil {
		return fmt.Errorf("create sysvision runtime directory: %w", err)
	}
	go d.bridgeServicectlEvents()
	go d.listenNotifydIngress()
	return d.serveAPI()
}

func (d *daemon) bridgeServicectlEvents() {
	for {
		if err := d.streamServicectlEvents(); err != nil {
			d.logger.Printf("servicectl event stream failed: %v", err)
			time.Sleep(time.Second)
		}
	}
}

func (d *daemon) streamServicectlEvents() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := d.servicectlRequest(ctx, "/v1/events")
	if err != nil {
		d.setServicectlEventsState(false, err.Error())
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("servicectl events returned %s", resp.Status)
		d.setServicectlEventsState(false, err.Error())
		return err
	}
	d.setServicectlEventsState(true, "")
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event visionapi.EventEnvelope
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		d.broadcast(event)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		d.setServicectlEventsState(false, err.Error())
		return err
	}
	d.setServicectlEventsState(false, io.EOF.Error())
	return io.EOF
}

func (d *daemon) listenNotifydIngress() {
	for {
		if err := d.listenNotifydIngressOnce(); err != nil {
			d.logger.Printf("notifyd ingress failed: %v", err)
			time.Sleep(time.Second)
		}
	}
}

func (d *daemon) listenNotifydIngressOnce() error {
	socketPath := visionapi.SysvisionIngressSocketPath(d.userMode, d.runtime)
	if err := visionapi.PrepareUnixDatagramListener(socketPath); err != nil {
		return err
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		return err
	}
	defer func() {
		_ = conn.Close()
		_ = os.Remove(socketPath)
	}()
	_ = os.Chmod(socketPath, 0660)
	buf := make([]byte, 65535)
	for {
		n, _, err := conn.ReadFromUnix(buf)
		if err != nil {
			return err
		}
		var event visionapi.EventEnvelope
		if err := json.Unmarshal(buf[:n], &event); err != nil {
			continue
		}
		d.broadcast(event)
	}
}

func (d *daemon) serveAPI() error {
	socketPath := visionapi.SysvisionSocketPath(d.userMode, d.runtime)
	if err := visionapi.PrepareUnixStreamListener(socketPath); err != nil {
		return fmt.Errorf("prepare sysvision api socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on sysvision api socket: %w", err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0660); err != nil {
		return fmt.Errorf("chmod sysvision api socket: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/meta", d.handleMeta)
	mux.HandleFunc("/v1/watch", d.handleWatch)
	mux.HandleFunc("/v1/query/units", d.handleUnitsQuery)
	mux.HandleFunc("/v1/query/unit/", d.handleUnitQuery)
	server := &http.Server{Handler: mux}
	return server.Serve(listener)
}

func (d *daemon) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, d.meta())
}

func (d *daemon) handleWatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	filter := visionapi.WatchFilter{
		Source: r.URL.Query().Get("source"),
		Kind:   r.URL.Query().Get("kind"),
		Unit:   r.URL.Query().Get("unit"),
	}
	id, ch := d.subscribe(filter)
	defer d.unsubscribe(id)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case event, ok := <-ch:
			if !ok {
				return
			}
			if err := json.NewEncoder(w).Encode(event); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (d *daemon) handleUnitsQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	resp, err := d.servicectlRequest(r.Context(), "/v1/units")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

func (d *daemon) handleUnitQuery(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Path[len("/v1/query/unit/"):]
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	resp, err := d.servicectlRequest(r.Context(), "/v1/unit/"+url.PathEscape(name))
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

func (d *daemon) subscribe(filter visionapi.WatchFilter) (int, chan visionapi.EventEnvelope) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.nextID++
	ch := make(chan visionapi.EventEnvelope, 32)
	d.subs[d.nextID] = subscriber{filter: filter, ch: ch}
	return d.nextID, ch
}

func (d *daemon) unsubscribe(id int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if sub, ok := d.subs[id]; ok {
		delete(d.subs, id)
		close(sub.ch)
	}
}

func (d *daemon) broadcast(event visionapi.EventEnvelope) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for id, sub := range d.subs {
		if !sub.filter.Matches(event) {
			continue
		}
		select {
		case sub.ch <- event:
		default:
			d.logger.Printf("dropping slow subscriber %d for %s %s", id, event.Source, event.Unit)
		}
	}
}

func (d *daemon) setServicectlEventsState(connected bool, errText string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servicectlEventsConnected = connected
	d.servicectlEventsError = errText
}

func (d *daemon) meta() metaResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	return metaResponse{
		ServicectlEventsConnected: d.servicectlEventsConnected,
		ServicectlEventsError:     d.servicectlEventsError,
	}
}

func (d *daemon) servicectlRequest(ctx context.Context, path string) (*http.Response, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", visionapi.ServicectlSocketPath(d.userMode, d.runtime))
		},
	}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func copyResponse(w http.ResponseWriter, resp *http.Response) {
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}
