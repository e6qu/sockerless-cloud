package main

// AWS Systems Manager Session Manager AgentMessage framing for the Amazon ECS
// ExecuteCommand WebSocket, whose server side speaks the Session Manager binary
// protocol. The 120-byte fixed header follows AWS's session-manager-plugin.

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

var (
	errSSMFrameTooShort  = errors.New("ssm frame: shorter than 120-byte fixed header")
	errSSMFrameTruncated = errors.New("ssm frame: truncated payload")
)

const (
	ssmHeaderLen        = 116
	ssmFixedHeaderLen   = ssmHeaderLen + 4 // 120 bytes through PayloadLength
	ssmMessageTypeLen   = 32
	ssmPayloadDigestLen = 32
	ssmFlagsSynFin      = uint64(3)
)

// MessageType values, right-padded with spaces to 32 bytes.
const (
	ssmMTOutputStreamData = "output_stream_data"
	ssmMTInputStreamData  = "input_stream_data"
	ssmMTChannelClosed    = "channel_closed"
)

// PayloadType values: 1 carries stdout, 11 stderr, and 12 the process exit
// code.
const (
	ssmPayloadStdout   = uint32(1)
	ssmPayloadStderr   = uint32(11)
	ssmPayloadExitCode = uint32(12)
)

// outputSequence numbers every outbound frame. Session Manager numbers frames
// per session; the simulator numbers them per process, which only a single
// session per process sees as equivalent.
var outputSequence int64

// buildSSMOutputFrame wraps a payload chunk in an output_stream_data
// AgentMessage of payloadType.
func buildSSMOutputFrame(payloadType uint32, payload []byte) []byte {
	digest := sha256.Sum256(payload)
	out := make([]byte, ssmFixedHeaderLen+len(payload))
	binary.BigEndian.PutUint32(out[0:4], ssmHeaderLen)

	mt := []byte(ssmMTOutputStreamData)
	copy(out[4:4+ssmMessageTypeLen], mt)
	for i := 4 + len(mt); i < 4+ssmMessageTypeLen; i++ {
		out[i] = ' '
	}

	binary.BigEndian.PutUint32(out[36:40], 1) // schema version
	binary.BigEndian.PutUint64(out[40:48], uint64(time.Now().UnixMilli()))
	seq := atomic.AddInt64(&outputSequence, 1)
	binary.BigEndian.PutUint64(out[48:56], uint64(seq))
	binary.BigEndian.PutUint64(out[56:64], ssmFlagsSynFin)

	id := uuid.New()
	idBytes, _ := id.MarshalBinary()
	// Write the UUID as AWS's Java putUuid does: bytes 8-15 at offset 64, bytes 0-7 at offset 72.
	copy(out[64:72], idBytes[8:16])
	copy(out[72:80], idBytes[0:8])

	copy(out[80:112], digest[:])
	binary.BigEndian.PutUint32(out[112:116], payloadType)
	binary.BigEndian.PutUint32(out[116:120], uint32(len(payload)))
	copy(out[ssmFixedHeaderLen:], payload)
	return out
}

// decodeSSMInputFrame parses an inbound AgentMessage into its payload, its
// MessageType (input_stream_data, acknowledge, ...) and whether it carries FIN.
// SSM Agent forwards only input_stream_data payloads to the process and closes
// its stdin after FIN, so binary stdin arrives intact and readers see EOF.
func decodeSSMInputFrame(b []byte) (payload []byte, messageType string, fin bool, err error) {
	// The frame comes off an untrusted WebSocket; FrameReader bounds every read so
	// a declared payload length cannot slice past the buffer.
	r := sim.NewFrameReader(b)
	header, herr := r.Take(ssmFixedHeaderLen)
	if herr != nil {
		return nil, "", false, errSSMFrameTooShort
	}
	mt := strings.TrimRight(string(header[4:4+ssmMessageTypeLen]), " \x00")
	flags := binary.BigEndian.Uint64(header[56:64])
	payloadLen := binary.BigEndian.Uint32(header[116:120])
	payload, perr := r.Take(int(payloadLen))
	if perr != nil {
		return nil, mt, false, errSSMFrameTruncated
	}
	return payload, mt, (flags & 2) != 0, nil
}

// buildSSMChannelClosed builds the empty channel_closed frame that ends the session.
func buildSSMChannelClosed() []byte {
	payload := []byte{}
	digest := sha256.Sum256(payload)
	out := make([]byte, ssmFixedHeaderLen)
	binary.BigEndian.PutUint32(out[0:4], ssmHeaderLen)

	mt := []byte(ssmMTChannelClosed)
	copy(out[4:4+ssmMessageTypeLen], mt)
	for i := 4 + len(mt); i < 4+ssmMessageTypeLen; i++ {
		out[i] = ' '
	}

	binary.BigEndian.PutUint32(out[36:40], 1)
	binary.BigEndian.PutUint64(out[40:48], uint64(time.Now().UnixMilli()))
	seq := atomic.AddInt64(&outputSequence, 1)
	binary.BigEndian.PutUint64(out[48:56], uint64(seq))
	binary.BigEndian.PutUint64(out[56:64], ssmFlagsSynFin)

	id := uuid.New()
	idBytes, _ := id.MarshalBinary()
	// Write the UUID as AWS's Java putUuid does: bytes 8-15 at offset 64, bytes 0-7 at offset 72.
	copy(out[64:72], idBytes[8:16])
	copy(out[72:80], idBytes[0:8])

	copy(out[80:112], digest[:])
	binary.BigEndian.PutUint32(out[112:116], 0) // PayloadType 0 for control
	binary.BigEndian.PutUint32(out[116:120], 0)
	return out
}
