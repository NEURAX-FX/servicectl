package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"
)

func main() {
	dumpFile := flag.String("dump-file", "", "path to write sorted environment")
	pidFile := flag.String("pid-file", "", "path to write current pid")
	readyMessage := flag.String("ready-message", "", "message to print after writing env")
	sleepSeconds := flag.Int("sleep-seconds", 300, "seconds to remain running")
	flag.Parse()

	if *pidFile != "" {
		if err := os.WriteFile(*pidFile, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "write pid file: %v\n", err)
			os.Exit(1)
		}
	}

	if *dumpFile != "" {
		env := os.Environ()
		sort.Strings(env)
		content := strings.Join(env, "\n") + "\n"
		if err := os.WriteFile(*dumpFile, []byte(content), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "write env dump: %v\n", err)
			os.Exit(1)
		}
	}

	if *readyMessage != "" {
		fmt.Println(*readyMessage)
	}

	if *sleepSeconds <= 0 {
		return
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	select {
	case <-sigCh:
	case <-time.After(time.Duration(*sleepSeconds) * time.Second):
	}
}
