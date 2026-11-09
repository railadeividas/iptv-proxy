package proxy

import (
	"io"
	"net/http"
	"strings"
)

// tsAudioFix repairs one known provider fault in MPEG-TS video: AAC audio whose frame headers (ADTS) claim 5.1 or
// 7.1 (channel configuration 4 to 7) while the frame data is plain stereo (it starts with a channel-pair element,
// but every real multichannel layout starts with a single-channel element). VLC copes with this; ffmpeg based
// players (Jellyfin, Plex, Emby) cannot decode such audio and fail. The fix rewrites the channel configuration in
// those headers to stereo, in place: no byte is added or removed, so lengths and timing stay as they are.
//
// It reads the stream packet by packet (188 bytes), learns the audio PIDs from the PAT and PMT, and looks at the
// first frame of every audio PES. Streams that are not MPEG-TS pass through untouched, as does any audio that is
// labelled correctly.
type tsAudioFix struct {
	src   io.Reader
	tmp   []byte
	buf   []byte // processed bytes ready for the reader
	rem   []byte // start of a packet that is not complete yet
	err   error
	pass  bool // not MPEG-TS (or lost sync): copy the rest as it is
	pmt   int  // PID of the program map, -1 until the PAT is seen
	audio map[int]*adtsState
}

// adtsState follows the ADTS frames inside the PES packets of one audio PID.
type adtsState struct {
	verdict int  // what the frames of this stream turned out to be: 1 stereo labelled as multichannel, -1 correct, 0 not known yet
	fix     bool // the frames of this PES need the repair
	pos     int  // byte offset in the current frame, -1 = out of sync until the next PES
	length  int  // frame length from its header
	cleared bool // this frame's channel configuration was changed
}

// fromStart reports whether the response is a whole MPEG-TS file (or its start): a plain 200, or a 206 that begins at
// byte 0. ffmpeg based players ask for "Range: bytes=0-" on every segment. A range from the middle of a file does
// not start at a packet boundary and has no PAT or PMT in front of it, so it is left alone.
func fromStart(in *http.Request, resp *http.Response) bool {
	switch resp.StatusCode {
	case http.StatusOK:
		return in == nil || in.Header.Get("Range") == "" || strings.HasPrefix(in.Header.Get("Range"), "bytes=0-")
	case http.StatusPartialContent:
		return strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes 0-")
	}
	return false
}

func newTSAudioFix(src io.Reader) *tsAudioFix {
	return &tsAudioFix{src: src, tmp: make([]byte, 188*64), pmt: -1, audio: map[int]*adtsState{}}
}

func (f *tsAudioFix) Read(p []byte) (int, error) {
	for len(f.buf) == 0 {
		if f.err != nil {
			return 0, f.err
		}
		n, err := f.src.Read(f.tmp)
		data := append(f.rem, f.tmp[:n]...)
		f.rem = nil
		if !f.pass && len(data) > 0 && data[0] != 0x47 {
			f.pass = true
		}
		i := 0
		for !f.pass && i+188 <= len(data) {
			if data[i] != 0x47 {
				f.pass = true
				break
			}
			f.packet(data[i : i+188])
			i += 188
		}
		if f.pass {
			f.buf = data
		} else {
			f.buf = data[:i]
			f.rem = append([]byte(nil), data[i:]...)
		}
		if err != nil {
			f.err = err
			f.buf = append(f.buf, f.rem...)
			f.rem = nil
		}
	}
	n := copy(p, f.buf)
	f.buf = f.buf[n:]
	return n, nil
}

// packet looks at one TS packet and repairs it in place when it carries audio that needs it.
func (f *tsAudioFix) packet(pk []byte) {
	pid := int(pk[1]&0x1f)<<8 | int(pk[2])
	pusi := pk[1]&0x40 != 0
	off := 4
	if pk[3]&0x20 != 0 { // adaptation field
		off += 1 + int(pk[4])
	}
	if pk[3]&0x10 == 0 || off >= 188 {
		return // no payload
	}
	pl := pk[off:]
	switch {
	case pid == 0 && pusi:
		f.readPAT(pl)
	case pid == f.pmt && pusi:
		f.readPMT(pl)
	default:
		if st := f.audio[pid]; st != nil {
			st.packet(pl, pusi)
		}
	}
}

func (f *tsAudioFix) readPAT(pl []byte) {
	if f.pmt >= 0 || len(pl) < 1 {
		return
	}
	pl = pl[1+int(pl[0]):] // pointer field
	if len(pl) < 12 || pl[0] != 0x00 {
		return
	}
	end := 3 + (int(pl[1]&0x0f)<<8 | int(pl[2])) - 4 // up to the CRC
	if end > len(pl) {
		end = len(pl)
	}
	for i := 8; i+4 <= end; i += 4 {
		if prog := int(pl[i])<<8 | int(pl[i+1]); prog != 0 {
			f.pmt = int(pl[i+2]&0x1f)<<8 | int(pl[i+3])
			return
		}
	}
}

func (f *tsAudioFix) readPMT(pl []byte) {
	if len(pl) < 1 {
		return
	}
	pl = pl[1+int(pl[0]):]
	if len(pl) < 13 || pl[0] != 0x02 {
		return
	}
	end := 3 + (int(pl[1]&0x0f)<<8 | int(pl[2])) - 4
	if end > len(pl) {
		end = len(pl)
	}
	i := 12 + (int(pl[10]&0x0f)<<8 | int(pl[11]))
	for i+5 <= end {
		typ, pid := pl[i], int(pl[i+1]&0x1f)<<8|int(pl[i+2])
		if typ == 0x0f && f.audio[pid] == nil { // AAC in ADTS
			f.audio[pid] = &adtsState{pos: -1}
		}
		i += 5 + (int(pl[i+3]&0x0f)<<8 | int(pl[i+4]))
	}
}

// packet handles the payload of one audio TS packet; pl is changed in place.
func (st *adtsState) packet(pl []byte, pusi bool) {
	if pusi {
		st.pos, st.fix, st.cleared = -1, false, false
		if len(pl) < 9 || pl[0] != 0 || pl[1] != 0 || pl[2] != 1 {
			return
		}
		hl := 9 + int(pl[8])
		if hl >= len(pl) {
			return
		}
		pl = pl[hl:]
		st.pos = 0
		st.fix = st.judge(pl)
	}
	if !st.fix {
		return
	}
	for i := 0; i < len(pl) && st.pos >= 0; i++ {
		b := pl[i]
		if st.pos < 6 {
			switch st.pos {
			case 0:
				if b != 0xff {
					st.pos = -1
					continue
				}
			case 1:
				if b&0xf6 != 0xf0 {
					st.pos = -1
					continue
				}
			case 2: // lowest bit: the top bit of the channel configuration
				st.cleared = b&1 == 1
				if st.cleared {
					pl[i] = b &^ 1
				}
			case 3: // top two bits: the rest of it; the low ones are the frame length
				st.length = int(b&3) << 11
				if st.cleared {
					pl[i] = b&0x3f | 0x80 // configuration 2: stereo
				}
			case 4:
				st.length |= int(b) << 3
			case 5:
				st.length |= int(b >> 5)
				if st.length < 7 {
					st.pos = -1
					continue
				}
			}
		}
		st.pos++
		if st.pos >= 6 && st.pos >= st.length {
			st.pos = 0
		}
	}
}

// judge decides whether the frames of this PES need the repair, from the header and first element of the first
// one. A frame that says nothing (it starts with data we cannot read) keeps the verdict the stream already has.
func (st *adtsState) judge(b []byte) bool {
	if len(b) < 10 || b[0] != 0xff || b[1]&0xf6 != 0xf0 {
		return false
	}
	cfg := int(b[2]&1)<<2 | int(b[3]>>6)
	if cfg < 4 {
		return false // stereo, mono or the like: nothing to repair
	}
	hdr := 7
	if b[1]&1 == 0 { // a CRC follows the header
		hdr = 9
	}
	switch id, ok := firstElement(b[hdr:]); {
	case !ok:
	case id == 1: // a channel pair first: no multichannel layout starts like that
		st.verdict = 1
	case id == 0 || id == 3: // a single channel or the low-frequency channel first, as multichannel layouts do
		st.verdict = -1
	}
	return st.verdict == 1
}

// firstElement is the id of the first channel element of a raw AAC frame, after any fill elements (id 6) that
// encoders put in front (for example their name and version).
func firstElement(b []byte) (int, bool) {
	bits := func(pos, n int) (int, bool) {
		if (pos+n+7)/8 > len(b) {
			return 0, false
		}
		v := 0
		for i := pos; i < pos+n; i++ {
			v = v<<1 | int(b[i/8]>>(7-i%8))&1
		}
		return v, true
	}
	pos := 0
	for range 4 {
		id, ok := bits(pos, 3)
		if !ok {
			return 0, false
		}
		if id != 6 {
			return id, true
		}
		n, ok := bits(pos+3, 4)
		if !ok {
			return 0, false
		}
		skip := 7
		if n == 15 {
			esc, ok := bits(pos+7, 8)
			if !ok {
				return 0, false
			}
			n, skip = 14+esc, 15
		}
		pos += skip + n*8
	}
	return 0, false
}
