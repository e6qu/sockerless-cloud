// Command inherit reports the sockets it inherited by LISTEN_FDS on standard
// output and serves one connection on the first TCP one.
package main

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

func main() {
	if os.Getenv("LISTEN_PID") != strconv.Itoa(os.Getpid()) {
		fmt.Printf("LISTEN_PID=%s, pid %d\n", os.Getenv("LISTEN_PID"), os.Getpid())
		os.Exit(1)
	}
	count, _ := strconv.Atoi(os.Getenv("LISTEN_FDS"))
	var stream net.Listener
	for fd := 3; fd < 3+count; fd++ {
		file := os.NewFile(uintptr(fd), "inherited")
		if ln, err := net.FileListener(file); err == nil {
			fmt.Printf("tcp %d\n", ln.Addr().(*net.TCPAddr).Port)
			if stream == nil {
				stream = ln
			}
		} else if conn, err := net.FilePacketConn(file); err == nil {
			fmt.Printf("udp %d\n", conn.LocalAddr().(*net.UDPAddr).Port)
		}
	}
	fmt.Println("ready")
	conn, err := stream.Accept()
	if err != nil {
		os.Exit(1)
	}
	_, _ = conn.Write([]byte("served\n"))
	_ = conn.Close()
	os.Exit(0)
}
