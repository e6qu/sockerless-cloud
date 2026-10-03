package main

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"strconv"
	"strings"
	"time"
)

// msRedisHandshakeDeadline bounds the TLS handshake of a client that connects
// and then says nothing.
const msRedisHandshakeDeadline = 30 * time.Second

// msRedisConnectPermission is what a principal needs to connect to a cluster
// that authenticates with IAM.
const msRedisConnectPermission = "redis.clusters.connect"

func (p *msRedisPlane) relayConnection(client net.Conn, target func() (int, bool)) {
	defer client.Close()
	p.mu.RLock()
	tlsConfig, iamAuth := p.tlsConfig, p.iamAuth
	p.mu.RUnlock()
	if tlsConfig != nil {
		server := tls.Server(client, tlsConfig)
		if err := server.SetDeadline(time.Now().Add(msRedisHandshakeDeadline)); err != nil {
			return
		}
		if err := server.Handshake(); err != nil {
			return
		}
		if err := server.SetDeadline(time.Time{}); err != nil {
			return
		}
		client = server
	}
	if err := p.Ensure(); err != nil {
		log.Printf("Memorystore %s data plane: %v", p.name, err)
		return
	}
	node, ok := target()
	if !ok {
		return
	}
	address, err := p.nodeAddress(node)
	if err != nil {
		log.Printf("Memorystore %s data plane: %v", p.name, err)
		return
	}
	backend, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		log.Printf("Memorystore %s data plane: dial node %d: %v", p.name, node, err)
		return
	}
	defer backend.Close()
	if iamAuth {
		p.relayAuthenticating(client, backend)
		return
	}
	msRedisRelay(client, backend)
}

type msRedisHalfCloser interface {
	CloseWrite() error
}

func msRedisRelay(left, right net.Conn) {
	done := make(chan struct{}, 2)
	copySide := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if closer, ok := dst.(msRedisHalfCloser); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}
	go copySide(left, right)
	go copySide(right, left)
	<-done
}

// relayAuthenticating relays a client of a cluster that authenticates with
// IAM. It reads each command the client sends, and an AUTH, or a HELLO that
// authenticates, whose password is an access token naming a principal that
// may connect, reaches the engine as the principal's engine user carrying the
// engine's own credential. Any other credential reaches the engine as the
// client sent it, which the engine refuses, so replies keep the order of the
// commands.
func (p *msRedisPlane) relayAuthenticating(client, backend net.Conn) {
	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(client, backend)
		if closer, ok := client.(msRedisHalfCloser); ok {
			_ = closer.CloseWrite()
		}
		done <- struct{}{}
	}()
	go func() {
		defer func() {
			if closer, ok := backend.(msRedisHalfCloser); ok {
				_ = closer.CloseWrite()
			}
			done <- struct{}{}
		}()
		reader := bufio.NewReader(client)
		for {
			args, raw, err := msRedisReadCommand(reader)
			if err != nil {
				return
			}
			if rewritten, ok := msRedisExchangeCredential(args, p.currentPassword(), p.connectAs); ok {
				raw = msRedisEncodeCommand(rewritten)
			}
			if _, err := backend.Write(raw); err != nil {
				return
			}
		}
	}()
	<-done
}

// connectAs is the engine user an access token this simulator issued
// authenticates as, when the principal it names holds redis.clusters.connect
// on the cluster: the ACL policy user its email names, or the default user.
func (p *msRedisPlane) connectAs(token string) (string, bool) {
	claims, err := verifiedAccessTokenClaims(token)
	if err != nil {
		return "", false
	}
	principal, owner := gcpSubjectPrincipal(claims.Sub)
	policies := gcpHierarchyPolicies(resourceProject(p.name))
	held := gcpPermissionsHeldUnder(principal, owner, policies, []string{msRedisConnectPermission}, gcpIAMResourceNamed(p.name))
	if len(held) != 1 {
		return "", false
	}
	return p.engineUserOf(claims.Sub), true
}

// msRedisExchangeCredential rewrites an AUTH, or a HELLO with AUTH, whose
// password authorize accepts, to authenticate as the engine user authorize
// names. The token decides the user, whatever username the client sent.
func msRedisExchangeCredential(args []string, secret string, authorize func(string) (string, bool)) ([]string, bool) {
	if len(args) == 0 {
		return nil, false
	}
	switch strings.ToUpper(args[0]) {
	case "AUTH":
		if len(args) < 2 || len(args) > 3 {
			return nil, false
		}
		user, ok := authorize(args[len(args)-1])
		if !ok {
			return nil, false
		}
		return []string{args[0], user, secret}, true
	case "HELLO":
		for i := 1; i+2 < len(args); i++ {
			if !strings.EqualFold(args[i], "AUTH") {
				continue
			}
			user, ok := authorize(args[i+2])
			if !ok {
				return nil, false
			}
			rewritten := append([]string(nil), args...)
			rewritten[i+1], rewritten[i+2] = user, secret
			return rewritten, true
		}
	}
	return nil, false
}

// msRedisReadCommand reads one command a client sends: a RESP array of bulk
// strings, or an inline command line. It returns the arguments and the bytes
// as they arrived.
func msRedisReadCommand(reader *bufio.Reader) ([]string, []byte, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, nil, err
	}
	if !strings.HasPrefix(line, "*") {
		return strings.Fields(line), []byte(line), nil
	}
	count, err := strconv.Atoi(strings.TrimRight(line[1:], "\r\n"))
	if err != nil {
		return nil, nil, fmt.Errorf("array length %q: %w", line, err)
	}
	raw := []byte(line)
	args := make([]string, 0, max(count, 0))
	for i := 0; i < count; i++ {
		header, err := reader.ReadString('\n')
		if err != nil {
			return nil, nil, err
		}
		if !strings.HasPrefix(header, "$") {
			return nil, nil, fmt.Errorf("command argument %q is not a bulk string", header)
		}
		size, err := strconv.Atoi(strings.TrimRight(header[1:], "\r\n"))
		if err != nil || size < 0 {
			return nil, nil, fmt.Errorf("bulk length %q", header)
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, nil, err
		}
		raw = append(raw, header...)
		raw = append(raw, data...)
		args = append(args, string(data[:size]))
	}
	return args, raw, nil
}
