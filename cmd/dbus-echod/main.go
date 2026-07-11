package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/godbus/dbus/v5"
)

type echoService struct{}

func (echoService) Echo(value string) (string, *dbus.Error) {
	return value, nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: dbus-echod <well-known-name>")
		os.Exit(2)
	}
	address := os.Getenv("DBUS_STARTER_ADDRESS")
	if address == "" {
		fmt.Fprintln(os.Stderr, "DBUS_STARTER_ADDRESS is required")
		os.Exit(2)
	}
	conn, err := dbus.Dial(address)
	if err != nil {
		panic(err)
	}
	defer conn.Close()
	if err := conn.Auth(nil); err != nil {
		panic(err)
	}
	if err := conn.Hello(); err != nil {
		panic(err)
	}
	if err := conn.Export(echoService{}, "/org/example/Echo", "org.example.Echo"); err != nil {
		panic(err)
	}
	reply, err := conn.RequestName(os.Args[1], dbus.NameFlagDoNotQueue)
	if err != nil {
		panic(err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		fmt.Fprintf(os.Stderr, "name request returned %d\n", reply)
		os.Exit(1)
	}
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
}
