// Package listenfds takes the listening sockets a process inherits through
// systemd's socket-activation protocol (sd_listen_fds(3)): LISTEN_PID names
// the process, LISTEN_FDS counts the sockets, and they start at file
// descriptor 3. A process that binds a configured port takes the inherited
// socket bound to that port instead, as sd_is_socket_inet(3) matches one, so
// whoever started it can hold the port across its restarts.
package listenfds

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"sync"
)

const firstFD = 3

var (
	loadOnce  sync.Once
	loadErr   error
	mu        sync.Mutex
	listeners []*net.TCPListener
	packets   []*net.UDPConn
)

// load takes the inherited sockets once and unsets the variables, as
// sd_listen_fds(1) does, so no process this one starts takes them as its own.
func load() {
	pid, count := os.Getenv("LISTEN_PID"), os.Getenv("LISTEN_FDS")
	for _, key := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		_ = os.Unsetenv(key)
	}
	if pid == "" || pid != strconv.Itoa(os.Getpid()) {
		return
	}
	n, err := strconv.Atoi(count)
	if err != nil || n < 0 {
		loadErr = fmt.Errorf("listenfds: LISTEN_FDS=%q is not a count", count)
		return
	}
	for fd := firstFD; fd < firstFD+n; fd++ {
		file := os.NewFile(uintptr(fd), "LISTEN_FD_"+strconv.Itoa(fd))
		loadErr = adopt(fd, file)
		_ = file.Close()
		if loadErr != nil {
			return
		}
	}
}

// adopt keeps the socket file holds as a TCP listener or a UDP socket; the
// net package duplicates the descriptor, so the caller closes file.
func adopt(fd int, file *os.File) error {
	if ln, err := net.FileListener(file); err == nil {
		tcp, ok := ln.(*net.TCPListener)
		if !ok {
			_ = ln.Close()
			return fmt.Errorf("listenfds: inherited descriptor %d listens on %s, not TCP", fd, ln.Addr().Network())
		}
		listeners = append(listeners, tcp)
		return nil
	}
	if conn, err := net.FilePacketConn(file); err == nil {
		udp, ok := conn.(*net.UDPConn)
		if !ok {
			_ = conn.Close()
			return fmt.Errorf("listenfds: inherited descriptor %d is a %s socket, not UDP", fd, conn.LocalAddr().Network())
		}
		packets = append(packets, udp)
		return nil
	}
	return fmt.Errorf("listenfds: inherited descriptor %d is neither a listening TCP socket nor a UDP socket", fd)
}

func port(addr net.Addr) int {
	switch a := addr.(type) {
	case *net.TCPAddr:
		return a.Port
	case *net.UDPAddr:
		return a.Port
	}
	return 0
}

// take removes and returns the first socket in held bound to wanted.
func take[T any](held *[]T, wanted int, addr func(T) net.Addr) (T, bool) {
	var zero T
	if wanted == 0 {
		return zero, false
	}
	for i, socket := range *held {
		if port(addr(socket)) == wanted {
			*held = append((*held)[:i], (*held)[i+1:]...)
			return socket, true
		}
	}
	return zero, false
}

// ListenTCP returns the inherited listening socket bound to laddr's port, or
// binds laddr when none is.
func ListenTCP(network string, laddr *net.TCPAddr) (*net.TCPListener, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	mu.Lock()
	ln, ok := take(&listeners, laddr.Port, func(l *net.TCPListener) net.Addr { return l.Addr() })
	mu.Unlock()
	if ok {
		return ln, nil
	}
	return net.ListenTCP(network, laddr)
}

// ListenUDP returns the inherited UDP socket bound to laddr's port, or binds
// laddr when none is.
func ListenUDP(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
	loadOnce.Do(load)
	if loadErr != nil {
		return nil, loadErr
	}
	mu.Lock()
	conn, ok := take(&packets, laddr.Port, func(c *net.UDPConn) net.Addr { return c.LocalAddr() })
	mu.Unlock()
	if ok {
		return conn, nil
	}
	return net.ListenUDP(network, laddr)
}

// Listen is net.Listen for a TCP address, taking an inherited socket bound to
// its port.
func Listen(network, address string) (net.Listener, error) {
	laddr, err := net.ResolveTCPAddr(network, address)
	if err != nil {
		return nil, err
	}
	return ListenTCP(network, laddr)
}
