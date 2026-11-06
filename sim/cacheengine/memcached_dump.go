package cacheengine

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// MemcachedItem is one item a Memcached node holds: its key, the client flags
// stored with it, when it expires (a Unix time, zero for never) and its value.
type MemcachedItem struct {
	Key     string `json:"key"`
	Flags   uint32 `json:"flags"`
	Expires int64  `json:"expires,omitempty"`
	Value   []byte `json:"value"`
}

// Dump reads every item the nodes hold: lru_crawler metadump lists each
// node's keys with their expiry, and a get reads each one's flags and value.
// An item that expires or is evicted between the two is left out, as it would
// be from any later read.
func (m *Memcached) Dump() ([]byte, error) {
	if err := m.Ensure(); err != nil {
		return nil, err
	}
	var items []MemcachedItem
	for _, index := range m.Nodes() {
		address, err := m.nodeAddress(index)
		if err != nil {
			return nil, err
		}
		held, err := dumpMemcachedNode(address)
		if err != nil {
			return nil, fmt.Errorf("dump node %d of the Memcached engine: %w", index, err)
		}
		items = append(items, held...)
	}
	return json.Marshal(items)
}

// Load stores every item of a dump, each on the node its key's position in
// the node order picks.
func (m *Memcached) Load(dump []byte) error {
	var items []MemcachedItem
	if err := json.Unmarshal(dump, &items); err != nil {
		return fmt.Errorf("read the Memcached dump: %w", err)
	}
	if err := m.Ensure(); err != nil {
		return err
	}
	nodes := m.Nodes()
	if len(nodes) == 0 {
		return fmt.Errorf("the Memcached engine runs no node")
	}
	conns := map[int]net.Conn{}
	defer func() {
		for _, conn := range conns {
			_ = conn.Close()
		}
	}()
	now := time.Now().Unix()
	for i, item := range items {
		if item.Expires != 0 && item.Expires <= now {
			continue
		}
		index := nodes[i%len(nodes)]
		conn, ok := conns[index]
		if !ok {
			address, err := m.nodeAddress(index)
			if err != nil {
				return err
			}
			if conn, err = net.DialTimeout("tcp", address, commandDeadline); err != nil {
				return err
			}
			conns[index] = conn
		}
		if err := conn.SetDeadline(time.Now().Add(commandDeadline)); err != nil {
			return err
		}
		// memcached reads an exptime above 30 days as a Unix time.
		if _, err := fmt.Fprintf(conn, "set %s %d %d %d\r\n%s\r\n", item.Key, item.Flags, item.Expires, len(item.Value), item.Value); err != nil {
			return err
		}
		reply, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			return err
		}
		if reply != "STORED\r\n" {
			return fmt.Errorf("store %q: %s", item.Key, strings.TrimSpace(reply))
		}
	}
	return nil
}

func dumpMemcachedNode(address string) ([]MemcachedItem, error) {
	conn, err := net.DialTimeout("tcp", address, commandDeadline)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(commandDeadline)); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	if _, err := io.WriteString(conn, "lru_crawler metadump all\r\n"); err != nil {
		return nil, err
	}
	expiries := map[string]int64{}
	var keys []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "END" {
			break
		}
		if strings.HasPrefix(line, "ERROR") || strings.HasPrefix(line, "BUSY") {
			return nil, fmt.Errorf("lru_crawler metadump: %s", line)
		}
		var key string
		var expires int64
		for _, field := range strings.Fields(line) {
			name, value, _ := strings.Cut(field, "=")
			switch name {
			case "key":
				if key, err = url.QueryUnescape(value); err != nil {
					return nil, fmt.Errorf("metadump key %q: %w", value, err)
				}
			case "exp":
				if expires, err = strconv.ParseInt(value, 10, 64); err != nil {
					return nil, fmt.Errorf("metadump exp %q: %w", value, err)
				}
			}
		}
		if key == "" {
			continue
		}
		if expires < 0 {
			expires = 0
		}
		expiries[key] = expires
		keys = append(keys, key)
	}
	items := make([]MemcachedItem, 0, len(keys))
	for _, key := range keys {
		if _, err := fmt.Fprintf(conn, "get %s\r\n", key); err != nil {
			return nil, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		fields := strings.Fields(line)
		if len(fields) == 1 && fields[0] == "END" {
			continue
		}
		if len(fields) != 4 || fields[0] != "VALUE" {
			return nil, fmt.Errorf("get %q answered %q", key, strings.TrimSpace(line))
		}
		flags, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			return nil, err
		}
		size, err := strconv.Atoi(fields[3])
		if err != nil {
			return nil, err
		}
		value := make([]byte, size+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		if end, err := reader.ReadString('\n'); err != nil || end != "END\r\n" {
			return nil, fmt.Errorf("get %q ended with %q: %v", key, end, err)
		}
		items = append(items, MemcachedItem{Key: key, Flags: uint32(flags), Expires: expiries[key], Value: value[:size]})
	}
	return items, nil
}
