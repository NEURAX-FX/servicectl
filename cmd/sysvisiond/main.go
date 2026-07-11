package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
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

	"servicectl/internal/util"
	"servicectl/internal/visionapi"
)

type subscriber struct {
	filter visionapi.WatchFilter
	ch     chan visionapi.EventEnvelope
}

type config struct {
	mode         string
	uid          uint32
	pollInterval time.Duration
}

type planeState struct {
	servicectlConnected bool
	servicectlErrorText string
}

type daemon struct {
	logger *log.Logger
	cfg    config
	epoch  string
	mu     sync.Mutex
	nextID int
	subs   map[int]subscriber
	plane  planeState
}

func main() {
	logger := log.New(os.Stdout, "sysvisiond: ", log.LstdFlags)
	cfg, err := parseConfig(os.Args[1:], os.Geteuid())
	if err != nil {
		logger.Fatal(err)
	}
	epoch, err := randomEpoch()
	if err != nil {
		logger.Fatal(err)
	}
	d := newDaemon(cfg, epoch, logger)
	if err := d.run(); err != nil {
		logger.Fatal(err)
	}
}

func parseConfig(args []string, euid int) (config, error) {
	fs := flag.NewFlagSet("sysvisiond", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	mode := fs.String("mode", visionapi.ModeSystem, "event plane: system or user")
	pollInterval := fs.Duration("poll-interval", 500*time.Millisecond, "unit snapshot poll interval")
	if err := fs.Parse(args); err != nil {
		return config{}, err
	}
	if fs.NArg() != 0 {
		return config{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	normalized := strings.ToLower(strings.TrimSpace(*mode))
	if normalized != visionapi.ModeSystem && normalized != visionapi.ModeUser {
		return config{}, fmt.Errorf("invalid mode %q", *mode)
	}
	if normalized == visionapi.ModeSystem && euid != 0 {
		return config{}, fmt.Errorf("system mode requires root")
	}
	if *pollInterval <= 0 {
		return config{}, fmt.Errorf("poll interval must be positive")
	}
	uid := uint32(0)
	if normalized == visionapi.ModeUser {
		if euid < 0 || uint64(euid) > uint64(^uint32(0)) {
			return config{}, fmt.Errorf("invalid effective uid %d", euid)
		}
		uid = uint32(euid)
	}
	return config{mode: normalized, uid: uid, pollInterval: *pollInterval}, nil
}

func randomEpoch() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func newDaemon(cfg config, epoch string, logger *log.Logger) *daemon {
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}
	return &daemon{logger: logger, cfg: cfg, epoch: epoch, subs: make(map[int]subscriber)}
}

func (d *daemon) run() error {
	if err := os.MkdirAll(visionapi.SysvisionDirForMode(d.cfg.mode), 0755); err != nil {
		return fmt.Errorf("create %s sysvision runtime directory: %w", d.cfg.mode, err)
	}
	errCh := make(chan error, 2)
	go d.bridgeServicectlEvents()
	go func() { errCh <- d.listenNotifydIngress() }()
	go func() { errCh <- d.serveAPI() }()
	err := <-errCh
	if err != nil {
		return err
	}
	return nil
}

func (d *daemon) bridgeServicectlEvents() {
	for {
		if err := d.streamServicectlEvents(); err != nil {
			d.logger.Printf("%s servicectl event stream failed: %v", d.cfg.mode, err)
			time.Sleep(time.Second)
		}
	}
}

func (d *daemon) streamServicectlEvents() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	resp, err := d.servicectlRequest(ctx, "/v1/events?mode="+url.QueryEscape(d.cfg.mode))
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
		if strings.TrimSpace(event.Mode) == "" {
			event.Mode = d.cfg.mode
		}
		event.UID = d.cfg.uid
		d.broadcast(event)
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		d.setServicectlEventsState(false, err.Error())
		return err
	}
	d.setServicectlEventsState(false, io.EOF.Error())
	return io.EOF
}

func (d *daemon) listenNotifydIngress() error {
	for {
		if err := d.listenNotifydIngressOnce(); err != nil {
			d.logger.Printf("%s notifyd ingress failed: %v", d.cfg.mode, err)
			time.Sleep(time.Second)
		}
	}
}

func (d *daemon) listenNotifydIngressOnce() error {
	socketPath := visionapi.SysvisionIngressSocketPathForMode(d.cfg.mode)
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
			event.Mode = d.cfg.mode
		}
		event.UID = d.cfg.uid
		d.broadcast(event)
	}
}

func (d *daemon) serveAPI() error {
	socketPath := visionapi.SysvisionSocketPathForMode(d.cfg.mode)
	if err := visionapi.PrepareUnixStreamListener(socketPath); err != nil {
		return fmt.Errorf("prepare %s sysvision api socket: %w", d.cfg.mode, err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s sysvision api socket: %w", d.cfg.mode, err)
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()
	if err := os.Chmod(socketPath, 0660); err != nil {
		return fmt.Errorf("chmod %s sysvision api socket: %w", d.cfg.mode, err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/meta", d.handleMeta)
	mux.HandleFunc("/v1/watch", d.handleWatch)
	mux.HandleFunc("/v1/query/units", d.handleUnitsQuery)
	mux.HandleFunc("/v1/query/unit/", d.handleUnitQuery)
	mux.HandleFunc("/v1/query/properties", func(w http.ResponseWriter, r *http.Request) { d.handlePropertyQuery(w, r, "/v1/properties") })
	mux.HandleFunc("/v1/query/groups", func(w http.ResponseWriter, r *http.Request) { d.handlePropertyQuery(w, r, "/v1/groups") })
	mux.HandleFunc("/v1/query/group/", func(w http.ResponseWriter, r *http.Request) {
		d.handlePropertyQuery(w, r, "/v1/group/"+strings.TrimPrefix(r.URL.Path, "/v1/query/group/"))
	})
	mux.HandleFunc("/v1/query/unit-groups/", func(w http.ResponseWriter, r *http.Request) {
		d.handlePropertyQuery(w, r, "/v1/unit-groups/"+strings.TrimPrefix(r.URL.Path, "/v1/query/unit-groups/"))
	})
	server := &http.Server{Handler: mux}
	return server.Serve(listener)
}

func (d *daemon) handleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	util.WriteJSON(w, d.meta())
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
		Mode:   util.FirstNonEmpty(strings.TrimSpace(r.URL.Query().Get("mode")), d.cfg.mode),
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

func (d *daemon) handlePropertyQuery(w http.ResponseWriter, r *http.Request, path string) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	resp, err := d.propertyRequest(r.Context(), path)
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
	// 256 is enough to absorb the startup event burst from a large dependency
	// graph (e.g. slurmctld pulling in 20+ deps) without the broadcaster having
	// to drop events on slow subscribers. The previous 32 overflowed and
	// triggered the "dropping slow subscriber" log spam during normal boots.
	ch := make(chan visionapi.EventEnvelope, 256)
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

func (d *daemon) setServicectlEventsState(connected bool, errText string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.plane.servicectlConnected = connected
	d.plane.servicectlErrorText = errText
}

func (d *daemon) meta() visionapi.MetaResponse {
	d.mu.Lock()
	defer d.mu.Unlock()
	return visionapi.MetaResponse{
		VisionEpoch:               d.epoch,
		Mode:                      d.cfg.mode,
		UID:                       d.cfg.uid,
		ServicectlEventsConnected: d.plane.servicectlConnected,
		ServicectlEventsError:     d.plane.servicectlErrorText,
	}
}

func (d *daemon) servicectlRequest(ctx context.Context, path string) (*http.Response, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", visionapi.ServicectlSocketPathForMode(d.cfg.mode))
		},
	}
	client := &http.Client{Transport: transport}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path, nil)
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (d *daemon) propertyRequest(ctx context.Context, path string) (*http.Response, error) {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network string, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", visionapi.SystemPropertySocketPath())
		},
	}
	client := &http.Client{Transport: transport}
	separator := "?"
	if strings.Contains(path, "?") {
		separator = "&"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://unix"+path+separator+"mode="+url.QueryEscape(d.cfg.mode), nil)
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
