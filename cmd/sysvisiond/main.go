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
	"strings"
	"sync"
	"time"

	"servicectl/internal/visionapi"
)

type subscriber struct {
	filter visionapi.WatchFilter
	ch     chan visionapi.EventEnvelope
}

type planeState struct {
	mode                string
	servicectlConnected bool
	servicectlErrorText string
}

type daemon struct {
	logger *log.Logger
	mu     sync.Mutex
	nextID int
	subs   map[int]subscriber
	plane  map[string]planeState
}

type metaResponse struct {
	SystemServicectlEventsConnected bool   `json:"system_servicectl_events_connected"`
	SystemServicectlEventsError     string `json:"system_servicectl_events_error,omitempty"`
	UserServicectlEventsConnected   bool   `json:"user_servicectl_events_connected"`
	UserServicectlEventsError       string `json:"user_servicectl_events_error,omitempty"`
}

func main() {
	flag.Parse()
	logger := log.New(os.Stdout, "sysvisiond: ", log.LstdFlags)
	d := &daemon{logger: logger, subs: make(map[int]subscriber), plane: map[string]planeState{
		visionapi.ModeSystem: {mode: visionapi.ModeSystem},
		visionapi.ModeUser:   {mode: visionapi.ModeUser},
	}}
	if err := d.run(); err != nil {
		logger.Fatal(err)
	}
}

func (d *daemon) run() error {
	for _, plane := range visionapi.Planes() {
		if err := os.MkdirAll(visionapi.SysvisionDirForMode(plane.Mode), 0755); err != nil {
			return fmt.Errorf("create %s sysvision runtime directory: %w", plane.Mode, err)
		}
	}
	errCh := make(chan error, 6)
	for _, plane := range visionapi.Planes() {
		plane := plane
		go d.bridgeServicectlEvents(plane.Mode)
		go func() { errCh <- d.listenNotifydIngress(plane.Mode) }()
		go func() { errCh <- d.serveAPI(plane.Mode) }()
	}
	err := <-errCh
	if err != nil {
		return err
	}
	return nil
}

func (d *daemon) bridgeServicectlEvents(mode string) {
	for {
		if err := d.streamServicectlEvents(mode); err != nil {
			d.logger.Printf("%s servicectl event stream failed: %v", mode, err)
			time.Sleep(time.Second)
		}
	}
}

func (d *daemon) streamServicectlEvents(mode string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := d.servicectlRequest(ctx, mode, "/v1/events?mode="+url.QueryEscape(mode))
	if err != nil {
		d.setServicectlEventsState(mode, false, err.Error())
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("servicectl events returned %s", resp.Status)
		d.setServicectlEventsState(mode, false, err.Error())
		return err
	}
	d.setServicectlEventsState(mode, true, "")
	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		var event visionapi.EventEnvelope
		if err := json.Unmarshal(line, &event); err != nil {
			continue
		}
		if strings.TrimSpace(event.Mode) == "" {
			event.Mode = mode
		}
		d.broadcast(event)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		d.setServicectlEventsState(mode, false, err.Error())
		return err
	}
	d.setServicectlEventsState(mode, false, io.EOF.Error())
	return io.EOF
}

func (d *daemon) listenNotifydIngress(mode string) error {
	for {
		if err := d.listenNotifydIngressOnce(mode); err != nil {
			d.logger.Printf("%s notifyd ingress failed: %v", mode, err)
			time.Sleep(time.Second)
		}
	}
}

func (d *daemon) listenNotifydIngressOnce(mode string) error {
	socketPath := visionapi.SysvisionIngressSocketPathForMode(mode)
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
		if strings.TrimSpace(event.Mode) == "" {
			event.Mode = mode
		}
		d.broadcast(event)
	}
}

func (d *daemon) serveAPI(mode string) error {
	socketPath := visionapi.SysvisionSocketPathForMode(mode)
	if err := visionapi.PrepareUnixStreamListener(socketPath); err != nil {
		return fmt.Errorf("prepare %s sysvision api socket: %w", mode, err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s sysvision api socket: %w", mode, err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0660); err != nil {
		return fmt.Errorf("chmod %s sysvision api socket: %w", mode, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/meta", d.handleMeta)
	mux.HandleFunc("/v1/watch", func(w http.ResponseWriter, r *http.Request) { d.handleWatch(mode, w, r) })
	mux.HandleFunc("/v1/query/units", func(w http.ResponseWriter, r *http.Request) { d.handleUnitsQuery(mode, w, r) })
	mux.HandleFunc("/v1/query/unit/", func(w http.ResponseWriter, r *http.Request) { d.handleUnitQuery(mode, w, r) })
	mux.HandleFunc("/v1/query/properties", func(w http.ResponseWriter, r *http.Request) { d.handlePropertyQuery(mode, w, r, "/v1/properties") })
	mux.HandleFunc("/v1/query/groups", func(w http.ResponseWriter, r *http.Request) { d.handlePropertyQuery(mode, w, r, "/v1/groups") })
	mux.HandleFunc("/v1/query/group/", func(w http.ResponseWriter, r *http.Request) {
		d.handlePropertyQuery(mode, w, r, "/v1/group/"+strings.TrimPrefix(r.URL.Path, "/v1/query/group/"))
	})
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

func (d *daemon) handleWatch(mode string, w http.ResponseWriter, r *http.Request) {
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
		Mode:   firstNonEmpty(strings.TrimSpace(r.URL.Query().Get("mode")), mode),
		Unit:   r.URL.Query().Get("unit"),
		Group:  r.URL.Query().Get("group"),
		Key:    r.URL.Query().Get("key"),
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

func (d *daemon) handleUnitsQuery(mode string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	resp, err := d.servicectlRequest(r.Context(), mode, "/v1/units")
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

func (d *daemon) handleUnitQuery(mode string, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	name := r.URL.Path[len("/v1/query/unit/"):]
	if name == "" {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	resp, err := d.servicectlRequest(r.Context(), mode, "/v1/unit/"+url.PathEscape(name))
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	copyResponse(w, resp)
}

func (d *daemon) handlePropertyQuery(mode string, w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	resp, err := d.propertyRequest(r.Context(), mode, path)
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
			d.logger.Printf("dropping slow subscriber %d for %s %s %s", id, event.Mode, event.Source, event.Unit)
		}
	}
}

func (d *daemon) setServicectlEventsState(mode string, connected bool, errText string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	state := d.plane[visionapi.PlaneForMode(mode).Mode]
	state.servicectlConnected = connected
	state.servicectlErrorText = errText
	d.plane[state.mode] = state
}

func (d *daemon) meta() metaResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	system := d.plane[visionapi.ModeSystem]
	user := d.plane[visionapi.ModeUser]
	return metaResponse{
		SystemServicectlEventsConnected: system.servicectlConnected,
		SystemServicectlEventsError:     system.servicectlErrorText,
		UserServicectlEventsConnected:   user.servicectlConnected,
		UserServicectlEventsError:       user.servicectlErrorText,
	}
}

func (d *daemon) servicectlRequest(ctx context.Context, mode string, path string) (*http.Response, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", visionapi.ServicectlSocketPathForMode(mode))
		},
	}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (d *daemon) propertyRequest(ctx context.Context, mode string, path string) (*http.Response, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", visionapi.PropertySocketPathForMode(mode))
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
