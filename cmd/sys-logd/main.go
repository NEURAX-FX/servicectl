package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"log/syslog"
	"os"
	"strings"
)

type config struct {
	service string
}

func main() {
	cfg := parseFlags()
	identifier := journalIdentifier(cfg.service)
	writer, err := syslog.New(syslog.LOG_INFO|syslog.LOG_DAEMON, identifier)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sys-logd: connect syslog socket: %v\n", err)
		os.Exit(1)
	}
	defer writer.Close()

	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			message := strings.TrimRight(line, "\r\n")
			if message != "" {
				if writeErr := writer.Info(message); writeErr != nil {
					fmt.Fprintf(os.Stderr, "sys-logd: write syslog message: %v\n", writeErr)
					os.Exit(1)
				}
			}
		}
		if err != nil {
			if err == io.EOF {
				return
			}
			fmt.Fprintf(os.Stderr, "sys-logd: read stdin: %v\n", err)
			os.Exit(1)
		}
	}
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.service, "service", "service", "logical service name for journal identifier")
	flag.Parse()
	return cfg
}

func journalIdentifier(service string) string {
	service = strings.TrimSpace(service)
	if service == "" {
		service = "service"
	}
	return "servicectl[" + service + "]"
}
