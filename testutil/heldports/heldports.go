// Package heldports binds the ports a test harness gives the processes it
// starts and holds them for the harness's life, handing the listening sockets
// down through systemd's socket-activation protocol (sd_listen_fds(3)). A port
// the harness never releases cannot be taken by another process between the
// pick and the bind, nor between a process's exit and its restart.
package heldports

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

var (
	mu   sync.Mutex
	held = map[int][]*os.File{}
)

func hold(port int, files ...*os.File) {
	mu.Lock()
	held[port] = append(held[port], files...)
	mu.Unlock()
}

// TCP binds a listening TCP socket on address, such as ":0" or
// "127.0.0.1:0", and returns its port.
func TCP(address string) (int, error) {
	laddr, err := net.ResolveTCPAddr("tcp", address)
	if err != nil {
		return 0, err
	}
	ln, err := net.ListenTCP("tcp", laddr)
	if err != nil {
		return 0, fmt.Errorf("bind %s: %w", address, err)
	}
	defer func() { _ = ln.Close() }()
	file, err := ln.File()
	if err != nil {
		return 0, err
	}
	bound, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		return 0, fmt.Errorf("a TCP listener reported address %v", ln.Addr())
	}
	port := bound.Port
	hold(port, file)
	return port, nil
}

// TCPUDP binds one IPv4 port on host, "" for every address, for both a
// listening TCP socket and a UDP socket, as a DNS server takes them, and
// returns it.
func TCPUDP(host string) (int, error) {
	return tcpUDP("4", host)
}

// DualStackTCPUDP binds one port on every IPv4 and IPv6 address for both a
// listening TCP socket and a UDP socket, as an HTTPS server serving HTTP/3
// takes them, and returns it.
func DualStackTCPUDP() (int, error) {
	return tcpUDP("", "")
}

func tcpUDP(family, host string) (int, error) {
	const attempts = 100
	for range attempts {
		udp, err := net.ListenUDP("udp"+family, &net.UDPAddr{IP: net.ParseIP(host)})
		if err != nil {
			return 0, fmt.Errorf("bind UDP on %q: %w", host, err)
		}
		bound, ok := udp.LocalAddr().(*net.UDPAddr)
		if !ok {
			_ = udp.Close()
			return 0, fmt.Errorf("a UDP socket reported address %v", udp.LocalAddr())
		}
		port := bound.Port
		tcp, err := net.ListenTCP("tcp"+family, &net.TCPAddr{IP: net.ParseIP(host), Port: port})
		if err != nil {
			_ = udp.Close()
			continue
		}
		udpFile, udpErr := udp.File()
		tcpFile, tcpErr := tcp.File()
		_ = udp.Close()
		_ = tcp.Close()
		if err := errors.Join(udpErr, tcpErr); err != nil {
			return 0, err
		}
		hold(port, tcpFile, udpFile)
		return port, nil
	}
	return 0, fmt.Errorf("no port was free on both TCP and UDP after %d attempts", attempts)
}

// listenPID sets LISTEN_PID to the started process's own ID before it
// executes the command: exec keeps the shell's process ID.
const listenPID = `LISTEN_PID=$$; export LISTEN_PID; exec "$0" "$@"`

// Hand passes cmd, before it starts, the sockets held on ports as inherited
// file descriptors from 3 on, with LISTEN_FDS and LISTEN_PID naming them.
func Hand(cmd *exec.Cmd, ports ...int) error {
	if cmd.Process != nil {
		return errors.New("hand sockets to a command before it starts")
	}
	if len(cmd.ExtraFiles) > 0 {
		return errors.New("the command already inherits files at descriptor 3 on")
	}
	mu.Lock()
	var files []*os.File
	for _, port := range ports {
		sockets, ok := held[port]
		if !ok {
			mu.Unlock()
			return fmt.Errorf("port %d is not held", port)
		}
		files = append(files, sockets...)
	}
	mu.Unlock()
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	kept := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, "LISTEN_PID=") && !strings.HasPrefix(entry, "LISTEN_FDS=") && !strings.HasPrefix(entry, "LISTEN_FDNAMES=") {
			kept = append(kept, entry)
		}
	}
	cmd.Env = append(kept, "LISTEN_FDS="+strconv.Itoa(len(files)))
	cmd.ExtraFiles = files
	cmd.Args = append([]string{"/bin/sh", "-c", listenPID, cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/bin/sh"
	return nil
}

// Release closes the harness's sockets on ports.
func Release(ports ...int) {
	mu.Lock()
	defer mu.Unlock()
	for _, port := range ports {
		for _, file := range held[port] {
			_ = file.Close()
		}
		delete(held, port)
	}
}
