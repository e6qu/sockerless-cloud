package realexec

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"time"
)

// The libpcap capture file format, written directly rather than through a
// third-party library: a capture is a 24-byte file header followed by one
// 16-byte record header and its bytes per frame. Every tool that reads a
// capture — tcpdump, Wireshark, the Azure portal's download — reads this
// format, so writing it faithfully is what makes a capture a real artifact
// rather than an opaque blob only this project can open.
//
// Reference: https://wiki.wireshark.org/Development/LibpcapFileFormat
const (
	// pcapMagicMicroseconds is the standard magic number, declaring
	// host-endian fields and microsecond timestamp resolution.
	pcapMagicMicroseconds = 0xa1b2c3d4
	pcapVersionMajor      = 2
	pcapVersionMinor      = 4

	// LinkTypeEthernet is the DLT the frames captured off a TAP or veth
	// interface carry: a full Ethernet II header ahead of the network-layer
	// payload.
	LinkTypeEthernet = 1

	pcapFileHeaderLen   = 24
	pcapRecordHeaderLen = 16
)

// PcapWriter serialises frames into the libpcap capture format. It is safe for
// concurrent use so a capture loop can write while a status read inspects the
// counters.
type PcapWriter struct {
	mu      sync.Mutex
	w       io.Writer
	snapLen uint32
	wrote   bool

	packets uint64
	bytes   uint64
}

// NewPcapWriter writes the file header and returns a writer ready for frames.
// snapLen is the per-frame capture limit in bytes; a frame longer than it is
// truncated in the file while its original length is preserved in the record,
// which is what a reader uses to report that the capture was cut short.
func NewPcapWriter(w io.Writer, snapLen int) (*PcapWriter, error) {
	if snapLen <= 0 {
		return nil, errors.New("pcap snap length must be positive")
	}
	header := make([]byte, pcapFileHeaderLen)
	binary.LittleEndian.PutUint32(header[0:4], pcapMagicMicroseconds)
	binary.LittleEndian.PutUint16(header[4:6], pcapVersionMajor)
	binary.LittleEndian.PutUint16(header[6:8], pcapVersionMinor)
	// thiszone (GMT offset) and sigfigs (timestamp accuracy) are zero in every
	// capture written in practice; timestamps are UTC.
	binary.LittleEndian.PutUint32(header[16:20], uint32(snapLen))
	binary.LittleEndian.PutUint32(header[20:24], LinkTypeEthernet)
	if _, err := w.Write(header); err != nil {
		return nil, err
	}
	return &PcapWriter{w: w, snapLen: uint32(snapLen), wrote: true}, nil
}

// WritePacket appends one frame captured at ts. originalLen is the frame's
// length on the wire, which may exceed len(frame) when the capture truncated
// it; passing zero means the frame was captured whole.
func (p *PcapWriter) WritePacket(ts time.Time, frame []byte, originalLen int) error {
	if originalLen <= 0 {
		originalLen = len(frame)
	}
	captured := frame
	if uint32(len(captured)) > p.snapLen {
		captured = captured[:p.snapLen]
	}
	record := make([]byte, pcapRecordHeaderLen)
	binary.LittleEndian.PutUint32(record[0:4], uint32(ts.Unix()))
	binary.LittleEndian.PutUint32(record[4:8], uint32(ts.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(record[8:12], uint32(len(captured)))
	binary.LittleEndian.PutUint32(record[12:16], uint32(originalLen))

	p.mu.Lock()
	defer p.mu.Unlock()
	if _, err := p.w.Write(record); err != nil {
		return err
	}
	if _, err := p.w.Write(captured); err != nil {
		return err
	}
	p.packets++
	p.bytes += uint64(pcapRecordHeaderLen + len(captured))
	return nil
}

// Counts reports how many frames have been written and how many bytes the
// capture file holds for them, excluding the file header. A capture bounded by
// a total-bytes limit measures itself with this.
func (p *PcapWriter) Counts() (packets uint64, bytes uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.packets, p.bytes
}
