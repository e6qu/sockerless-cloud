package main

// SSM Session Manager AgentMessage emitter for the ECS exec WebSocket.
// The backend (backends/ecs/exec_cloud.go) expects the server side of
// the exec channel to speak the AWS Session Manager binary protocol:
// real AWS does, so the simulator has to as well. Wire layout
// matches backends/ecs/ssm_proto.go — see that file + AWS's
// session-manager-plugin source for the 120-byte fixed header format.

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

// MessageType constants (right-padded with spaces to 32 bytes).
const (
	ssmMTOutputStreamData = "output_stream_data"
	ssmMTInputStreamData  = "input_stream_data"
	ssmMTChannelClosed    = "channel_closed"
)

// PayloadType constants. Backend decoder maps 1 → Docker stream 1
// (stdout) and 11 → stream 2 (stderr); 12 signals process exit.
const (
	ssmPayloadStdout   = uint32(1)
	ssmPayloadStderr   = uint32(11)
	ssmPayloadExitCode = uint32(12)
)

// outputSequence is a monotonic per-process counter applied to every
// emitted output_stream_data frame. Real AWS uses a per-session
// counter; the simulator uses process-global which is equivalent for
// single-session tests (what the e2e suite exercises).
var outputSequence int64

// buildSSMOutputFrame wraps a payload chunk in an output_stream_data
// AgentMessage. payloadType selects stdout (1), stderr (11), or
// exit-code (12).
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
	// AWS Java-style putUuid: LSL (UUID bytes 8-15) at offset 64, MSL (bytes 0-7) at offset 72.
	copy(out[64:72], idBytes[8:16]) // LSL
	copy(out[72:80], idBytes[0:8])  // MSL

	copy(out[80:112], digest[:])
	binary.BigEndian.PutUint32(out[112:116], payloadType)
	binary.BigEndian.PutUint32(out[116:120], uint32(len(payload)))
	copy(out[ssmFixedHeaderLen:], payload)
	return out
}

// decodeSSMInputFrame parses an inbound AgentMessage from the backend's
// stdin pipe and returns the payload bytes, the frame's MessageType
// (`input_stream_data` for stdin data, `acknowledge` for sequence acks,
// etc), and whether the FIN flag is set (indicating no more frames will
// follow). Real ssm-agent only forwards `input_stream_data` payloads to
// the user process and closes the user's stdin after the FIN frame; the
// simulator must do the same so binary stdin (tar archives, gzip-
// compressed input, etc) reaches the process intact, and tar / cat /
// other readers see EOF when the backend is done sending.
func decodeSSMInputFrame(b []byte) (payload []byte, messageType string, fin bool, err error) {
	// The frame comes off an attacker-controlled WebSocket; FrameReader bounds
	// every read so a giant/overflowing payload length can't slice past the
	// buffer (no manual length arithmetic to get wrong).
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

// buildSSMChannelClosed builds the channel_closed frame that tells the
// backend decoder to terminate cleanly. Payload can be empty.
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
	// AWS Java-style putUuid: LSL (UUID bytes 8-15) at offset 64, MSL (bytes 0-7) at offset 72.
	copy(out[64:72], idBytes[8:16]) // LSL
	copy(out[72:80], idBytes[0:8])  // MSL

	copy(out[80:112], digest[:])
	binary.BigEndian.PutUint32(out[112:116], 0) // PayloadType 0 for control
	binary.BigEndian.PutUint32(out[116:120], 0)
	return out
}
