package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log/syslog"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	defaultQueueSize         = 1024
	defaultEnqueueTimeout    = 10 * time.Millisecond
	defaultReconnectInterval = time.Second
	defaultSpillDir          = "/run/servicectl/logspill"
	defaultSpillMaxBytes     = 16 << 20
	defaultSpillRotations    = 4
)

type config struct {
	service           string
	queueSize         int
	enqueueTimeout    time.Duration
	reconnectInterval time.Duration
	spillDir          string
	spillMaxBytes     int64
	spillRotations    int
}

type statusReporter struct {
	service  string
	mu       sync.Mutex
	spilling bool
}

func main() {
	cfg := parseFlags()
	identifier := journalIdentifier(cfg.service)
	reporter := &statusReporter{service: cfg.service}
	spill := newSpillManager(cfg.service, cfg.spillDir, cfg.spillMaxBytes, cfg.spillRotations)
	var spillMode atomic.Bool

	messages := make(chan string, cfg.queueSize)
	errCh := make(chan error, 1)
	go readInput(messages, errCh, cfg, spill, reporter, &spillMode)

	runWriter(messages, errCh, identifier, cfg, spill, reporter, &spillMode)
}

func runWriter(messages <-chan string, errCh <-chan error, identifier string, cfg config, spill *spillManager, reporter *statusReporter, spillMode *atomic.Bool) {
	var writer *syslog.Writer
	defer closeWriter(writer)

	inputClosed := false
	for {
		if spillMode.Load() && len(messages) == 0 {
			if err := replaySpill(&writer, identifier, cfg, spill, reporter); err == nil {
				if !spill.HasSpill() {
					spillMode.Store(false)
					reporter.exitSpill()
				}
			} else {
				reporter.enterSpill("syslog unavailable: " + err.Error())
				time.Sleep(cfg.reconnectInterval)
			}
			if inputClosed && len(messages) == 0 && !spillMode.Load() {
				return
			}
			continue
		}

		if inputClosed {
			if spillMode.Load() {
				time.Sleep(cfg.reconnectInterval)
				continue
			}
			return
		}

		select {
		case msg, ok := <-messages:
			if !ok {
				inputClosed = true
				select {
				case err := <-errCh:
					if err != nil && err != io.EOF {
						fmt.Fprintf(os.Stderr, "sys-logd: read stdin: %v\n", err)
					}
				default:
				}
				continue
			}
			if err := writeMessage(&writer, identifier, msg); err != nil {
				spillMode.Store(true)
				reporter.enterSpill("syslog write failed: " + err.Error())
				if spillErr := spill.WriteLine(msg); spillErr != nil {
					reporter.spillFailure(spillErr)
				}
			}
		case err := <-errCh:
			if err != nil && err != io.EOF {
				fmt.Fprintf(os.Stderr, "sys-logd: read stdin: %v\n", err)
			}
		}
	}
}

func readInput(messages chan<- string, errCh chan<- error, cfg config, spill *spillManager, reporter *statusReporter, spillMode *atomic.Bool) {
	defer close(messages)
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			message := strings.TrimRight(line, "\r\n")
			if message != "" {
				routeMessage(messages, message, cfg, spill, reporter, spillMode)
			}
		}
		if err != nil {
			errCh <- err
			return
		}
	}
}

func routeMessage(messages chan<- string, message string, cfg config, spill *spillManager, reporter *statusReporter, spillMode *atomic.Bool) {
	if spillMode.Load() {
		if err := spill.WriteLine(message); err != nil {
			reporter.spillFailure(err)
		}
		return
	}

	timer := time.NewTimer(cfg.enqueueTimeout)
	defer timer.Stop()
	select {
	case messages <- message:
		return
	case <-timer.C:
		spillMode.Store(true)
		reporter.enterSpill("queue saturated")
		if err := spill.WriteLine(message); err != nil {
			reporter.spillFailure(err)
		}
	}
}

func replaySpill(writer **syslog.Writer, identifier string, cfg config, spill *spillManager, reporter *statusReporter) error {
	for spill.HasSpill() {
		reporter.replayStart()
		if err := spill.ReplayTo(func(message string) error {
			return writeMessage(writer, identifier, message)
		}); err != nil {
			return err
		}
	}
	return nil
}

func writeMessage(writer **syslog.Writer, identifier string, message string) error {
	if strings.TrimSpace(message) == "" {
		return nil
	}
	if err := ensureWriter(writer, identifier); err != nil {
		return err
	}
	if err := (*writer).Info(message); err != nil {
		closeWriter(*writer)
		*writer = nil
		return err
	}
	return nil
}

func ensureWriter(writer **syslog.Writer, identifier string) error {
	if *writer != nil {
		return nil
	}
	w, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, identifier)
	if err != nil {
		return err
	}
	*writer = w
	return nil
}

func closeWriter(writer *syslog.Writer) {
	if writer != nil {
		_ = writer.Close()
	}
}

func (r *statusReporter) enterSpill(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.spilling {
		return
	}
	r.spilling = true
	fmt.Fprintf(os.Stderr, "sys-logd: entering spill mode for %s: %s\n", r.service, reason)
}

func (r *statusReporter) replayStart() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.spilling {
		return
	}
	fmt.Fprintf(os.Stderr, "sys-logd: replaying buffered logs for %s\n", r.service)
}

func (r *statusReporter) exitSpill() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.spilling {
		return
	}
	r.spilling = false
	fmt.Fprintf(os.Stderr, "sys-logd: spill recovered for %s\n", r.service)
}

func (r *statusReporter) spillFailure(err error) {
	fmt.Fprintf(os.Stderr, "sys-logd: spill write failed for %s: %v\n", r.service, err)
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.service, "service", "service", "logical service name for journal identifier")
	flag.Parse()
	cfg.queueSize = maxInt(1, envInt("SERVICECTL_LOGD_QUEUE_SIZE", defaultQueueSize))
	cfg.enqueueTimeout = envDurationMillis("SERVICECTL_LOGD_ENQUEUE_TIMEOUT_MS", defaultEnqueueTimeout)
	cfg.reconnectInterval = envDurationMillis("SERVICECTL_LOGD_RECONNECT_INTERVAL_MS", defaultReconnectInterval)
	cfg.spillDir = envString("SERVICECTL_LOGSPILL_DIR", defaultSpillDir)
	cfg.spillMaxBytes = maxInt64(1024, envInt64("SERVICECTL_LOGD_SPILL_MAX_BYTES", defaultSpillMaxBytes))
	cfg.spillRotations = maxInt(1, envInt("SERVICECTL_LOGD_SPILL_ROTATIONS", defaultSpillRotations))
	return cfg
}

func journalIdentifier(service string) string {
	service = strings.TrimSpace(service)
	if service == "" {
		service = "service"
	}
	return "servicectl[" + service + "]"
}

func envString(name string, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envInt(name string, fallback int) int {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func envInt64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDurationMillis(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return time.Duration(parsed) * time.Millisecond
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a int64, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
