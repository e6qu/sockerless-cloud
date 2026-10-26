package cacheengine

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Conn is a client connection speaking RESP2, enough for the control plane's
// own commands.
type Conn struct {
	net.Conn
	reader *bufio.Reader
}

// Error is an error reply from the engine.
type Error string

func (e Error) Error() string { return string(e) }

// Dial connects to a Redis engine at address and authenticates with password
// when one is set.
func Dial(address, password string, timeout time.Duration) (*Conn, error) {
	conn, err := net.DialTimeout("tcp", address, timeout)
	if err != nil {
		return nil, err
	}
	return open(conn, "", password, timeout)
}

// DialTLS connects over TLS, authenticating as user (the default user when
// empty) when password is set.
func DialTLS(address string, config *tls.Config, user, password string, timeout time.Duration) (*Conn, error) {
	dialer := &net.Dialer{Timeout: timeout}
	conn, err := tls.DialWithDialer(dialer, "tcp", address, config)
	if err != nil {
		return nil, err
	}
	return open(conn, user, password, timeout)
}

func open(conn net.Conn, user, password string, timeout time.Duration) (*Conn, error) {
	c := &Conn{Conn: conn, reader: bufio.NewReader(conn)}
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if password != "" {
		args := []string{"AUTH", password}
		if user != "" {
			args = []string{"AUTH", user, password}
		}
		if _, err := c.Do(args...); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	return c, nil
}

// Do sends one command and reads its reply: a string for a status or bulk
// reply, an int64 for an integer, a []any for an array and nil for a null.
func (c *Conn) Do(args ...string) (any, error) {
	if _, err := c.Write(EncodeCommand(args)); err != nil {
		return nil, err
	}
	return ReadReply(c.reader)
}

// Pipeline sends every command before reading any reply and returns the
// replies in order. An error reply fails the call once every reply is read,
// so the connection stays in step, unless it starts with one of tolerated.
func (c *Conn) Pipeline(commands [][]string, tolerated ...string) ([]any, error) {
	var b bytes.Buffer
	for _, args := range commands {
		b.Write(EncodeCommand(args))
	}
	if _, err := c.Write(b.Bytes()); err != nil {
		return nil, err
	}
	replies := make([]any, len(commands))
	var failed error
	for i, args := range commands {
		reply, err := ReadReply(c.reader)
		var refused Error
		switch {
		case errors.As(err, &refused):
			if failed == nil && !toleratedRefusal(refused, tolerated) {
				failed = fmt.Errorf("%s: %w", strings.Join(args, " "), err)
			}
		case err != nil:
			return nil, err
		default:
			replies[i] = reply
		}
	}
	return replies, failed
}

func toleratedRefusal(refused Error, tolerated []string) bool {
	for _, prefix := range tolerated {
		if strings.HasPrefix(string(refused), prefix) {
			return true
		}
	}
	return false
}

// EncodeCommand encodes a command as a RESP array of bulk strings.
func EncodeCommand(args []string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, arg := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(arg), arg)
	}
	return b.Bytes()
}

// ReadReply reads one RESP2 reply.
func ReadReply(reader *bufio.Reader) (any, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if line == "" {
		return nil, fmt.Errorf("empty RESP reply")
	}
	payload := line[1:]
	switch line[0] {
	case '+':
		return payload, nil
	case '-':
		return nil, Error(payload)
	case ':':
		return strconv.ParseInt(payload, 10, 64)
	case '$':
		size, err := strconv.Atoi(payload)
		if err != nil {
			return nil, fmt.Errorf("bulk length %q: %w", payload, err)
		}
		if size < 0 {
			return nil, nil
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(reader, data); err != nil {
			return nil, err
		}
		return string(data[:size]), nil
	case '*':
		count, err := strconv.Atoi(payload)
		if err != nil {
			return nil, fmt.Errorf("array length %q: %w", payload, err)
		}
		if count < 0 {
			return nil, nil
		}
		items := make([]any, count)
		for i := range items {
			if items[i], err = ReadReply(reader); err != nil {
				return nil, err
			}
		}
		return items, nil
	}
	return nil, fmt.Errorf("unknown RESP reply %q", line)
}

// ReadCommand reads one command a client sends: a RESP array of bulk
// strings, or an inline command line. It returns the arguments and the bytes
// as they arrived.
func ReadCommand(reader *bufio.Reader) ([]string, []byte, error) {
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

// ParseInfo reads the fields of an INFO or CLUSTER INFO reply.
func ParseInfo(text string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), ":")
		if found {
			fields[key] = value
		}
	}
	return fields
}

// rdbMagic opens every RDB file.
const rdbMagic = "REDIS"

// IsRDB reports whether data is an RDB file.
func IsRDB(data []byte) bool {
	return bytes.HasPrefix(data, []byte(rdbMagic))
}
