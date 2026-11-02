package cacheengine

import (
	"bufio"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

// memcachedBinaryMagic opens every binary-protocol request.
const memcachedBinaryMagic = 0x80

// memcachedNoop is the meta no-op a node answers with "MN\r\n" in turn, which
// holds an answered command's place among the node's replies.
const memcachedNoop = "mn\r\n"

// relayAnswering relays a text-protocol connection, answering each command
// the plane's Answer takes in turn with the node's replies: the node gets a
// meta no-op in its place, and its reply to that no-op is swapped for the
// answer. A binary-protocol connection relays untouched.
func (m *Memcached) relayAnswering(client, backend net.Conn) {
	clientReader := bufio.NewReader(client)
	first, err := clientReader.Peek(1)
	if err != nil {
		return
	}
	peeked := &peekedConn{Conn: client, reader: clientReader}
	if first[0] == memcachedBinaryMagic {
		Relay(peeked, backend)
		return
	}
	var mu sync.Mutex
	// One entry per no-op the node will answer, in order: an answer to swap
	// in, or nil for a no-op the client sent itself.
	var pending [][]byte
	push := func(answer []byte) {
		mu.Lock()
		pending = append(pending, answer)
		mu.Unlock()
	}
	pop := func() ([]byte, bool) {
		mu.Lock()
		defer mu.Unlock()
		if len(pending) == 0 {
			return nil, false
		}
		head := pending[0]
		pending = pending[1:]
		return head, true
	}
	done := make(chan struct{}, 2)
	go func() {
		defer func() {
			if closer, ok := backend.(halfCloser); ok {
				_ = closer.CloseWrite()
			}
			done <- struct{}{}
		}()
		for {
			line, err := clientReader.ReadString('\n')
			if err != nil {
				return
			}
			fields := strings.Fields(line)
			if len(fields) > 0 {
				if answer, ok := m.cfg.Answer(fields); ok {
					push(answer)
					if _, err := io.WriteString(backend, memcachedNoop); err != nil {
						return
					}
					continue
				}
				if fields[0] == "mn" {
					push(nil)
				}
			}
			if _, err := io.WriteString(backend, line); err != nil {
				return
			}
			if size, ok := memcachedRequestDataSize(fields); ok {
				if _, err := io.CopyN(backend, clientReader, size+2); err != nil {
					return
				}
			}
		}
	}()
	go func() {
		defer func() {
			if closer, ok := client.(halfCloser); ok {
				_ = closer.CloseWrite()
			}
			done <- struct{}{}
		}()
		backendReader := bufio.NewReader(backend)
		for {
			line, err := backendReader.ReadString('\n')
			if err != nil {
				return
			}
			if line == "MN\r\n" {
				if answer, ok := pop(); ok && answer != nil {
					line = string(answer)
				}
			}
			if _, err := io.WriteString(client, line); err != nil {
				return
			}
			if size, ok := memcachedReplyDataSize(strings.Fields(line)); ok {
				if _, err := io.CopyN(client, backendReader, size+2); err != nil {
					return
				}
			}
		}
	}()
	<-done
}

// memcachedRequestDataSize is the length of the data block a storage command
// carries after its line.
func memcachedRequestDataSize(fields []string) (int64, bool) {
	if len(fields) == 0 {
		return 0, false
	}
	switch fields[0] {
	case "set", "add", "replace", "append", "prepend", "cas":
		return memcachedSize(fields, 4)
	case "ms":
		return memcachedSize(fields, 2)
	}
	return 0, false
}

// memcachedReplyDataSize is the length of the data block a value reply
// carries after its line.
func memcachedReplyDataSize(fields []string) (int64, bool) {
	if len(fields) == 0 {
		return 0, false
	}
	switch fields[0] {
	case "VALUE":
		return memcachedSize(fields, 3)
	case "VA":
		return memcachedSize(fields, 1)
	}
	return 0, false
}

func memcachedSize(fields []string, index int) (int64, bool) {
	if len(fields) <= index {
		return 0, false
	}
	size, err := strconv.ParseInt(fields[index], 10, 64)
	return size, err == nil && size >= 0
}
