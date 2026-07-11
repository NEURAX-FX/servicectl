package helper_test

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"servicectl/internal/dbusactivation"
)

func TestHelperProtocolAndEnvironmentFiltering(t *testing.T) {
	tempDir := t.TempDir()
	socketPath := filepath.Join(tempDir, "control.sock")
	helper := buildHelper(t, socketPath)
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: socketPath, Net: "unixpacket"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := listener.AcceptUnix()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		hello, err := readPacket(conn)
		if err != nil {
			serverErr <- err
			return
		}
		if hello.Type != dbusactivation.MessageHello || len(hello.Payload) != 1 || hello.Payload[0] != byte(dbusactivation.FrontendDaemonHelper) {
			serverErr <- errUnexpected("hello")
			return
		}
		if err := writePacket(conn, dbusactivation.Packet{Type: dbusactivation.MessageHello, RequestID: hello.RequestID, Payload: hello.Payload}); err != nil {
			serverErr <- err
			return
		}
		environmentPacket, err := readPacket(conn)
		if err != nil {
			serverErr <- err
			return
		}
		environment, err := dbusactivation.DecodeSetEnvironment(environmentPacket.Payload)
		if err != nil {
			serverErr <- err
			return
		}
		if environment.Values["LANG"] != "C.UTF-8" {
			serverErr <- errUnexpected("LANG")
			return
		}
		for _, key := range []string{"LD_PRELOAD", "DBUS_SESSION_BUS_ADDRESS", "DISPLAY", "SSH_AUTH_SOCK", "PYTHONPATH"} {
			if _, exists := environment.Values[key]; exists {
				serverErr <- errUnexpected(key)
				return
			}
		}
		if err := writeResult(conn, environmentPacket.RequestID, dbusactivation.ResultSuccess); err != nil {
			serverErr <- err
			return
		}
		activationPacket, err := readPacket(conn)
		if err != nil {
			serverErr <- err
			return
		}
		activation, err := dbusactivation.DecodeActivate(activationPacket.Payload)
		if err != nil || activation.BusName != "org.example.Service" {
			serverErr <- errUnexpected("activation")
			return
		}
		serverErr <- writeResult(conn, activationPacket.RequestID, dbusactivation.ResultSuccess)
	}()

	cmd := exec.Command(helper, "org.example.Service")
	cmd.Env = append(os.Environ(),
		"LANG=C.UTF-8",
		"LD_PRELOAD=/tmp/evil.so",
		"DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/session",
		"DISPLAY=:0",
		"SSH_AUTH_SOCK=/tmp/agent",
		"PYTHONPATH=/tmp/python",
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper failed: %v: %s", err, output)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestHelperMapsActivationResult(t *testing.T) {
	tests := []struct {
		code dbusactivation.ResultCode
		exit int
	}{
		{dbusactivation.ResultExecFailed, 9},
		{dbusactivation.ResultChildSignaled, 11},
		{dbusactivation.ResultUnknownService, 6},
		{dbusactivation.ResultTimeout, 1},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("code-%d", tt.code), func(t *testing.T) {
			tempDir := t.TempDir()
			socketPath := filepath.Join(tempDir, "control.sock")
			helper := buildHelper(t, socketPath)
			listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: socketPath, Net: "unixpacket"})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go serveResult(listener, tt.code)
			cmd := exec.Command(helper, "org.example.Service")
			err = cmd.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != tt.exit {
				t.Fatalf("exit = %v, want %d", err, tt.exit)
			}
		})
	}
}

func TestHelperRejectsInvalidArgumentsAndBusName(t *testing.T) {
	helper := buildHelper(t, filepath.Join(t.TempDir(), "unused.sock"))
	for _, args := range [][]string{{}, {"invalid"}, {"org.example.Service", "extra"}} {
		cmd := exec.Command(helper, args...)
		err := cmd.Run()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("args %#v unexpectedly succeeded", args)
		}
		want := 10
		if len(args) == 1 {
			want = 5
		}
		if exitErr.ExitCode() != want {
			t.Fatalf("args %#v exit = %d, want %d", args, exitErr.ExitCode(), want)
		}
	}
}

func buildHelper(t *testing.T, socketPath string) string {
	t.Helper()
	output := filepath.Join(t.TempDir(), "sys-dbusd-daemon-helper")
	command := exec.Command("cc", "-DSDBUSD_TESTING=1", `-DSDBUSD_CONTROL_PATH="`+socketPath+`"`, "-std=c17", "-Wall", "-Wextra", "-Werror", "-o", output, "src/main.c")
	command.Dir = "."
	if data, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile helper: %v: %s", err, data)
	}
	return output
}

func serveResult(listener *net.UnixListener, code dbusactivation.ResultCode) {
	conn, err := listener.AcceptUnix()
	if err != nil {
		return
	}
	defer conn.Close()
	hello, _ := readPacket(conn)
	_ = writePacket(conn, dbusactivation.Packet{Type: dbusactivation.MessageHello, RequestID: hello.RequestID, Payload: hello.Payload})
	environment, _ := readPacket(conn)
	_ = writeResult(conn, environment.RequestID, dbusactivation.ResultSuccess)
	activation, _ := readPacket(conn)
	_ = writeResult(conn, activation.RequestID, code)
}

func readPacket(conn *net.UnixConn) (dbusactivation.Packet, error) {
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	buffer := make([]byte, dbusactivation.HeaderSize+int(dbusactivation.MaxPayload)+1)
	n, err := conn.Read(buffer)
	if err != nil {
		return dbusactivation.Packet{}, err
	}
	return dbusactivation.DecodePacket(buffer[:n])
}

func writePacket(conn *net.UnixConn, packet dbusactivation.Packet) error {
	data, err := dbusactivation.EncodePacket(packet)
	if err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}

func writeResult(conn *net.UnixConn, requestID uint64, code dbusactivation.ResultCode) error {
	payload, err := dbusactivation.EncodeActivationResult(dbusactivation.ActivationResult{Code: code})
	if err != nil {
		return err
	}
	return writePacket(conn, dbusactivation.Packet{Type: dbusactivation.MessageActivationResult, RequestID: requestID, Payload: payload})
}

type unexpectedError string

func (e unexpectedError) Error() string { return string(e) }
func errUnexpected(value string) error  { return unexpectedError("unexpected " + value) }
