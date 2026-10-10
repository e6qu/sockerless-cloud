package lbplane

import (
	"crypto/rand"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
)

// ClientAddress splits the address the client connected from into its IP and
// port. An address that is not host:port comes back whole as the IP.
func ClientAddress(r *http.Request) (ip, port string) {
	ip, port, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr, ""
	}
	return ip, port
}

// AppendToHeaderList appends value to the list the header carries, as one
// field: the client's own values come first, each separated by separator.
func AppendToHeaderList(header http.Header, name, separator, value string) {
	if values := header.Values(name); len(values) > 0 {
		value = strings.Join(values, separator) + separator + value
	}
	header.Set(name, value)
}

// RandomHex returns n random bytes as 2n lower-case hexadecimal digits.
func RandomHex(n int) string {
	b := make([]byte, n)
	// crypto/rand.Read never returns an error; it crashes the program instead.
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
