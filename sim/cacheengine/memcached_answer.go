package cacheengine

import (
	"bufio"
	"encoding/binary"
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

// relayAnswering relays a connection, answering each command the plane's
// Answer takes in turn with the node's replies: the node gets a no-op in its
// place, and its reply to that no-op is swapped for the answer.
func (m *Memcached) relayAnswering(client, backend net.Conn) {
	clientReader := bufio.NewReader(client)
	first, err := clientReader.Peek(1)
	if err != nil {
		return
	}
	peeked := &peekedConn{Conn: client, reader: clientReader}
	if first[0] == memcachedBinaryMagic {
		m.relayAnsweringBinary(peeked, clientReader, backend)
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

// Binary-protocol framing: a 24-byte header, then extras, key and value.
const (
	memcachedBinaryHeader         = 24
	memcachedBinaryResponseMagic  = 0x81
	memcachedBinaryNoop           = 0x0a
	memcachedBinaryConfigGet      = 0x60
	memcachedBinaryConfigFlagsLen = 4
)

// relayAnsweringBinary answers each binary "config get" request the plane's
// Answer takes, the opcode and response layout of the ElastiCache Cluster
// Client's GetConfigOperationImpl: the node gets a no-op with the request's
// opaque in its place, and its reply to that no-op is swapped for a config
// response carrying the answer's flags and data.
func (m *Memcached) relayAnsweringBinary(client net.Conn, clientReader *bufio.Reader, backend net.Conn) {
	var mu sync.Mutex
	answers := map[uint32][]byte{}
	done := make(chan struct{}, 2)
	go func() {
		defer func() {
			if closer, ok := backend.(halfCloser); ok {
				_ = closer.CloseWrite()
			}
			done <- struct{}{}
		}()
		for {
			header, body, err := readMemcachedBinaryPacket(clientReader)
			if err != nil {
				return
			}
			if header[1] == memcachedBinaryConfigGet {
				extras := int(header[4])
				keyLength := int(binary.BigEndian.Uint16(header[2:4]))
				if extras+keyLength <= len(body) {
					key := string(body[extras : extras+keyLength])
					if answer, ok := m.cfg.Answer([]string{"config", "get", key}); ok {
						if flags, data, ok := parseMemcachedConfigAnswer(answer); ok {
							opaque := binary.BigEndian.Uint32(header[12:16])
							mu.Lock()
							answers[opaque] = memcachedBinaryConfigResponse(opaque, flags, data)
							mu.Unlock()
							noop := make([]byte, memcachedBinaryHeader)
							noop[0], noop[1] = memcachedBinaryMagic, memcachedBinaryNoop
							copy(noop[12:16], header[12:16])
							if _, err := backend.Write(noop); err != nil {
								return
							}
							continue
						}
					}
				}
			}
			if _, err := backend.Write(append(header, body...)); err != nil {
				return
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
			header, body, err := readMemcachedBinaryPacket(backendReader)
			if err != nil {
				return
			}
			packet := append(header, body...)
			if header[1] == memcachedBinaryNoop {
				opaque := binary.BigEndian.Uint32(header[12:16])
				mu.Lock()
				if answer, ok := answers[opaque]; ok {
					packet = answer
					delete(answers, opaque)
				}
				mu.Unlock()
			}
			if _, err := client.Write(packet); err != nil {
				return
			}
		}
	}()
	<-done
}

func readMemcachedBinaryPacket(reader *bufio.Reader) ([]byte, []byte, error) {
	header := make([]byte, memcachedBinaryHeader)
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, nil, err
	}
	body := make([]byte, binary.BigEndian.Uint32(header[8:12]))
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, nil, err
	}
	return header, body, nil
}

// parseMemcachedConfigAnswer reads the flags and data of a text-protocol
// "CONFIG <key> <flags> <bytes>" answer.
func parseMemcachedConfigAnswer(answer []byte) (uint32, []byte, bool) {
	line, rest, found := strings.Cut(string(answer), "\r\n")
	fields := strings.Fields(line)
	if !found || len(fields) != 4 || fields[0] != "CONFIG" {
		return 0, nil, false
	}
	flags, err := strconv.ParseUint(fields[2], 10, 32)
	if err != nil {
		return 0, nil, false
	}
	size, err := strconv.Atoi(fields[3])
	if err != nil || size < 0 || size > len(rest) {
		return 0, nil, false
	}
	return uint32(flags), []byte(rest[:size]), true
}

func memcachedBinaryConfigResponse(opaque, flags uint32, data []byte) []byte {
	packet := make([]byte, memcachedBinaryHeader+memcachedBinaryConfigFlagsLen+len(data))
	packet[0], packet[1] = memcachedBinaryResponseMagic, memcachedBinaryConfigGet
	packet[4] = memcachedBinaryConfigFlagsLen
	binary.BigEndian.PutUint32(packet[8:12], uint32(memcachedBinaryConfigFlagsLen+len(data)))
	binary.BigEndian.PutUint32(packet[12:16], opaque)
	binary.BigEndian.PutUint32(packet[24:28], flags)
	copy(packet[28:], data)
	return packet
}
