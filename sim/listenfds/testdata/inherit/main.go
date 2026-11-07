// Command inherit binds, through listenfds, the TCP and UDP ports LISTENFDS_CHILD
// names and an unheld port, reports which sockets it got, and serves one
// connection on the TCP one.
package main

import (
	"fmt"
	"net"
	"os"

	"github.com/e6qu/sockerless-cloud/sim/listenfds"
)

func main() {
	ports := os.Getenv("LISTENFDS_CHILD")
	var tcpPort, udpPort int
	if _, err := fmt.Sscanf(ports, "%d %d", &tcpPort, &udpPort); err != nil {
		os.Exit(2)
	}
	tcp, err := listenfds.ListenTCP("tcp", &net.TCPAddr{Port: tcpPort})
	if err != nil {
		fmt.Println("tcp:", err)
		os.Exit(1)
	}
	udp, err := listenfds.ListenUDP("udp", &net.UDPAddr{Port: udpPort})
	if err != nil {
		fmt.Println("udp:", err)
		os.Exit(1)
	}
	fresh, err := listenfds.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("fresh:", err)
		os.Exit(1)
	}
	fmt.Printf("tcp %d udp %d fresh %t env %q\n", tcp.Addr().(*net.TCPAddr).Port, udp.LocalAddr().(*net.UDPAddr).Port,
		fresh.Addr().(*net.TCPAddr).Port != tcpPort, os.Getenv("LISTEN_FDS")+os.Getenv("LISTEN_PID"))
	conn, err := tcp.Accept()
	if err != nil {
		os.Exit(1)
	}
	_, _ = conn.Write([]byte("served\n"))
	_ = conn.Close()
	os.Exit(0)
}
