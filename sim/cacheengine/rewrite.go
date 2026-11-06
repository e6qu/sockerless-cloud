package cacheengine

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
)

// commandQueue holds the commands a client sent whose replies the relay has
// not read yet, in order, so each reply can be matched to its command.
type commandQueue struct {
	mu   sync.Mutex
	sent [][]string
	// subscribed marks a connection in RESP2 subscription mode, where the
	// engine sends messages no command asked for and replies no longer pair
	// with commands.
	subscribed bool
}

func (q *commandQueue) push(args []string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(args) > 0 {
		switch strings.ToUpper(args[0]) {
		case "SUBSCRIBE", "PSUBSCRIBE", "SSUBSCRIBE", "MONITOR":
			q.subscribed = true
		}
	}
	q.sent = append(q.sent, args)
}

// pop returns the command the next reply answers, or false once replies no
// longer pair with commands.
func (q *commandQueue) pop() ([]string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.subscribed || len(q.sent) == 0 {
		return nil, false
	}
	args := q.sent[0]
	q.sent = q.sent[1:]
	return args, true
}

// namesNodes reports whether a command's reply names cluster nodes by address.
func namesNodes(args []string) bool {
	if len(args) < 2 || !strings.EqualFold(args[0], "CLUSTER") {
		return false
	}
	switch strings.ToUpper(args[1]) {
	case "SLOTS", "SHARDS", "NODES", "REPLICAS", "SLAVES":
		return true
	}
	return false
}

// relayReplies copies the engine's replies to the client, rewriting the node
// addresses a redirection or a topology reply names to their endpoints'.
func relayReplies(client net.Conn, backend net.Conn, commands *commandQueue, addresses func() map[string]string) error {
	reader := bufio.NewReader(backend)
	for {
		reply, err := readValue(reader)
		if err != nil {
			return err
		}
		if reply.kind != '>' {
			args, paired := commands.pop()
			switch {
			case reply.kind == '-' && (strings.HasPrefix(reply.text, "MOVED ") || strings.HasPrefix(reply.text, "ASK ")):
				reply.rewrite(addresses())
			case paired && namesNodes(args):
				reply.rewrite(addresses())
			}
		}
		var b bytes.Buffer
		reply.encode(&b)
		if _, err := client.Write(b.Bytes()); err != nil {
			return err
		}
	}
}

// respValue is one RESP2 or RESP3 reply as the engine sent it.
type respValue struct {
	kind  byte
	text  string
	null  bool
	items []respValue
	// next is the reply an attribute ('|') annotates.
	next *respValue
}

func readValue(reader *bufio.Reader) (respValue, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return respValue{}, err
	}
	line = strings.TrimSuffix(line, "\r\n")
	if line == "" {
		return respValue{}, fmt.Errorf("empty RESP reply")
	}
	v := respValue{kind: line[0], text: line[1:]}
	switch v.kind {
	case '+', '-', ':', '_', ',', '#', '(':
		return v, nil
	case '$', '=', '!':
		size, err := strconv.Atoi(v.text)
		if err != nil {
			return respValue{}, fmt.Errorf("bulk length %q: %w", v.text, err)
		}
		if size < 0 {
			v.null, v.text = true, ""
			return v, nil
		}
		data := make([]byte, size+2)
		if _, err := io.ReadFull(reader, data); err != nil {
			return respValue{}, err
		}
		v.text = string(data[:size])
		return v, nil
	case '*', '~', '>', '%', '|':
		count, err := strconv.Atoi(v.text)
		if err != nil {
			return respValue{}, fmt.Errorf("aggregate length %q: %w", v.text, err)
		}
		if count < 0 {
			v.null = true
			return v, nil
		}
		if v.kind == '%' || v.kind == '|' {
			count *= 2
		}
		v.items = make([]respValue, count)
		for i := range v.items {
			if v.items[i], err = readValue(reader); err != nil {
				return respValue{}, err
			}
		}
		if v.kind == '|' {
			next, err := readValue(reader)
			if err != nil {
				return respValue{}, err
			}
			v.next = &next
		}
		return v, nil
	}
	return respValue{}, fmt.Errorf("unknown RESP reply %q", line)
}

func (v respValue) encode(b *bytes.Buffer) {
	b.WriteByte(v.kind)
	switch v.kind {
	case '$', '=', '!':
		if v.null {
			b.WriteString("-1\r\n")
			return
		}
		fmt.Fprintf(b, "%d\r\n%s\r\n", len(v.text), v.text)
	case '*', '~', '>', '%', '|':
		if v.null {
			b.WriteString("-1\r\n")
			return
		}
		count := len(v.items)
		if v.kind == '%' || v.kind == '|' {
			count /= 2
		}
		fmt.Fprintf(b, "%d\r\n", count)
		for _, item := range v.items {
			item.encode(b)
		}
		if v.next != nil {
			v.next.encode(b)
		}
	default:
		b.WriteString(v.text)
		b.WriteString("\r\n")
	}
}

// rewrite replaces each node address the value names, as a whole string or
// as the host of a host:port, with its endpoint's address.
func (v *respValue) rewrite(addresses map[string]string) {
	switch v.kind {
	case '+', '-', '$', '=':
		v.text = rewriteAddresses(v.text, addresses)
	}
	for i := range v.items {
		v.items[i].rewrite(addresses)
	}
	if v.next != nil {
		v.next.rewrite(addresses)
	}
}

func rewriteAddresses(text string, addresses map[string]string) string {
	if endpoint, ok := addresses[text]; ok {
		return endpoint
	}
	for ip, endpoint := range addresses {
		if strings.HasPrefix(text, ip+":") {
			text = endpoint + text[len(ip):]
		}
		for _, separator := range []string{" ", "\n", ","} {
			text = strings.ReplaceAll(text, separator+ip+":", separator+endpoint+":")
		}
	}
	return text
}
