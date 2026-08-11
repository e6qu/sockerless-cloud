package realexec

import (
	"bytes"
	"encoding/binary"
	"testing"
	"time"
)

// The capture file has to be readable by tcpdump and Wireshark, so these
// assertions read the bytes back at the offsets the libpcap format specifies
// rather than through the writer that produced them.

func TestPcapWriterEmitsAReadableFileHeader(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewPcapWriter(&buf, 65535); err != nil {
		t.Fatalf("NewPcapWriter: %v", err)
	}
	b := buf.Bytes()
	if len(b) != pcapFileHeaderLen {
		t.Fatalf("file header is %d bytes, want %d", len(b), pcapFileHeaderLen)
	}
	if magic := binary.LittleEndian.Uint32(b[0:4]); magic != pcapMagicMicroseconds {
		t.Errorf("magic = %#x, want %#x", magic, pcapMagicMicroseconds)
	}
	if major := binary.LittleEndian.Uint16(b[4:6]); major != pcapVersionMajor {
		t.Errorf("version major = %d, want %d", major, pcapVersionMajor)
	}
	if snap := binary.LittleEndian.Uint32(b[16:20]); snap != 65535 {
		t.Errorf("snap length = %d, want 65535", snap)
	}
	if link := binary.LittleEndian.Uint32(b[20:24]); link != LinkTypeEthernet {
		t.Errorf("link type = %d, want %d (Ethernet)", link, LinkTypeEthernet)
	}
}

func TestPcapWriterRejectsAnUnusableSnapLength(t *testing.T) {
	var buf bytes.Buffer
	if _, err := NewPcapWriter(&buf, 0); err == nil {
		t.Fatal("a zero snap length must be rejected, not written as a header no reader can use")
	}
}

func TestPcapWriterRecordsFramesAndTruncatesToSnapLength(t *testing.T) {
	var buf bytes.Buffer
	const snapLen = 8
	w, err := NewPcapWriter(&buf, snapLen)
	if err != nil {
		t.Fatalf("NewPcapWriter: %v", err)
	}
	ts := time.Unix(1_700_000_000, 123_456_000).UTC()
	frame := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	if err := w.WritePacket(ts, frame, 0); err != nil {
		t.Fatalf("WritePacket: %v", err)
	}

	rec := buf.Bytes()[pcapFileHeaderLen:]
	if got := binary.LittleEndian.Uint32(rec[0:4]); got != uint32(ts.Unix()) {
		t.Errorf("timestamp seconds = %d, want %d", got, ts.Unix())
	}
	if got := binary.LittleEndian.Uint32(rec[4:8]); got != 123_456 {
		t.Errorf("timestamp microseconds = %d, want 123456", got)
	}
	if got := binary.LittleEndian.Uint32(rec[8:12]); got != snapLen {
		t.Errorf("captured length = %d, want %d (truncated to the snap length)", got, snapLen)
	}
	// The original length is preserved even though the frame was truncated —
	// that is how a reader reports the capture was cut short rather than
	// believing the frame really was eight bytes.
	if got := binary.LittleEndian.Uint32(rec[12:16]); got != uint32(len(frame)) {
		t.Errorf("original length = %d, want %d", got, len(frame))
	}
	if body := rec[pcapRecordHeaderLen:]; !bytes.Equal(body, frame[:snapLen]) {
		t.Errorf("frame body = %v, want the first %d bytes %v", body, snapLen, frame[:snapLen])
	}
}

func TestPcapWriterCountsWhatItWrote(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewPcapWriter(&buf, 65535)
	if err != nil {
		t.Fatalf("NewPcapWriter: %v", err)
	}
	frame := make([]byte, 60)
	for range 3 {
		if err := w.WritePacket(time.Now(), frame, 0); err != nil {
			t.Fatalf("WritePacket: %v", err)
		}
	}
	packets, written := w.Counts()
	if packets != 3 {
		t.Errorf("packets = %d, want 3", packets)
	}
	// A total-bytes bound measures itself with this, so it counts the record
	// headers as well as the frames.
	if want := uint64(3 * (pcapRecordHeaderLen + 60)); written != want {
		t.Errorf("bytes = %d, want %d", written, want)
	}
}
