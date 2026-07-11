package main

import "strings"

var version = "devel"

func versionString() string {
	value := strings.TrimSpace(version)
	if value == "" {
		return "devel"
	}
	return value
}
