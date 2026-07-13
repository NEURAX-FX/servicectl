//go:build linux

package main

import (
	"syscall"
	"unsafe"
)

type terminalWindowSize struct {
	rows    uint16
	columns uint16
	xpixel  uint16
	ypixel  uint16
}

func terminalWidth(fd uintptr) int {
	var size terminalWindowSize
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(syscall.TIOCGWINSZ), uintptr(unsafe.Pointer(&size)))
	if errno != 0 || size.columns == 0 {
		return 80
	}
	return int(size.columns)
}
