package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/iotest"
	"time"
)

// ---- a tiny MPEG-TS builder: PAT, PMT (one video and one AAC stream) and audio PES packets ----

func tsPacket(pid int, pusi bool, payload []byte) []byte {
	p := make([]byte, 188)
	p[0], p[1], p[2], p[3] = 0x47, byte(pid>>8)&0x1f, byte(pid), 0x10
	if pusi {
		p[1] |= 0x40
	}
	for i := range p[4:] {
		p[4+i] = 0xff
	}
	if len(payload) < 184 { // stuff the rest with an adaptation field
		p[3] = 0x30
		pad := 184 - len(payload) - 1
		p[4] = byte(pad)
		if pad > 0 {
			p[5] = 0
			for i := 1; i < pad; i++ {
				p[5+i] = 0xff
			}
		}
		copy(p[5+pad:], payload)
		return p
	}
	copy(p[4:], payload)
	return p
}

func tsPSI(pid int, table []byte) []byte { return tsPacket(pid, true, append([]byte{0}, table...)) }

func tsHeader() []byte {
	pat := []byte{0x00, 0xb0, 13, 0, 1, 0xc1, 0, 0, 0, 1, 0xe0 | 0x10, 0x00, 0, 0, 0, 0}
	pmt := []byte{0x02, 0xb0, 23, 0, 1, 0xc1, 0, 0, 0xe1, 0x00, 0xf0, 0, 0x1b, 0xe0, 0xd3, 0xf0, 0, 0x0f, 0xe0, 0xdd, 0xf0, 0, 0, 0, 0, 0}
	return append(tsPSI(0, pat), tsPSI(0x1000, pmt)...)
}

// adts builds one frame: protection absent, 48 kHz, the given channel configuration, then the first byte of the data.
func adts(cfg int, first byte, total int) []byte {
	f := make([]byte, total)
	f[0], f[1] = 0xff, 0xf1
	f[2] = 0x4c | byte(cfg>>2)&1 // LC, 48 kHz (index 3), top bit of cfg
	f[3] = byte(cfg&3)<<6 | byte(total>>11)&3
	f[4] = byte(total >> 3)
	f[5] = byte(total&7)<<5 | 0x1f
	f[6] = 0xfc
	f[7] = first
	for i := 8; i < total; i++ {
		f[i] = 0x55
	}
	return f
}

// audioPES splits frames into TS packets of the audio PID (0xdd), the first one carrying the PES header.
func audioPES(frames ...[]byte) []byte {
	es := bytes.Join(frames, nil)
	pes := append([]byte{0, 0, 1, 0xc0, 0, 0, 0x80, 0x80, 5, 0x21, 0, 1, 0, 1}, es...)
	var out []byte
	for first := true; len(pes) > 0; first = false {
		n := min(184, len(pes))
		out = append(out, tsPacket(0xdd, first, pes[:n])...)
		pes = pes[n:]
	}
	return out
}

func channelConfigs(ts []byte) []int {
	var cfgs []int
	var es []byte
	for i := 0; i+188 <= len(ts); i += 188 {
		p := ts[i : i+188]
		if int(p[1]&0x1f)<<8|int(p[2]) != 0xdd {
			continue
		}
		off := 4
		if p[3]&0x20 != 0 {
			off += 1 + int(p[4])
		}
		pl := p[off:]
		if p[1]&0x40 != 0 {
			pl = pl[9+int(pl[8]):]
		}
		es = append(es, pl...)
	}
	for i := 0; i+7 < len(es); {
		cfgs = append(cfgs, int(es[i+2]&1)<<2|int(es[i+3]>>6))
		i += int(es[i+3]&3)<<11 | int(es[i+4])<<3 | int(es[i+5]>>5)
	}
	return cfgs
}

func readAll(t *testing.T, r io.Reader) []byte {
	t.Helper()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTSAudioFixRepairsStereoLabelledAs51(t *testing.T) {
	// frames of different sizes so headers land across packet boundaries; the first element is a channel pair (0x21)
	in := append(tsHeader(), audioPES(adts(6, 0x21, 356), adts(6, 0x21, 347), adts(6, 0x21, 400), adts(6, 0x21, 360), adts(6, 0x21, 350))...)
	want := []int{2, 2, 2, 2, 2}
	for name, wrap := range map[string]func(io.Reader) io.Reader{
		"whole":     func(r io.Reader) io.Reader { return r },
		"one byte":  iotest.OneByteReader,
		"half-read": iotest.HalfReader,
	} {
		out := readAll(t, newTSAudioFix(wrap(bytes.NewReader(in))))
		if len(out) != len(in) {
			t.Fatalf("%s: the length must not change: %d != %d", name, len(out), len(in))
		}
		if got := channelConfigs(out); !equalInts(got, want) {
			t.Fatalf("%s: channel configs %v, want %v", name, got, want)
		}
		// nothing but the two header bytes of each frame may differ
		diff := 0
		for i := range in {
			if in[i] != out[i] {
				diff++
			}
		}
		if diff > 2*len(want) {
			t.Fatalf("%s: %d bytes changed", name, diff)
		}
	}
}

func TestTSAudioFixLeavesRealMultichannelAlone(t *testing.T) {
	// a real 5.1 frame starts with a single-channel element (id 0)
	in := append(tsHeader(), audioPES(adts(6, 0x01, 356), adts(6, 0x01, 347))...)
	out := readAll(t, newTSAudioFix(bytes.NewReader(in)))
	if !bytes.Equal(in, out) {
		t.Fatal("correctly labelled 5.1 must not change")
	}
	// stereo labelled as stereo
	in = append(tsHeader(), audioPES(adts(2, 0x21, 356))...)
	if out := readAll(t, newTSAudioFix(bytes.NewReader(in))); !bytes.Equal(in, out) {
		t.Fatal("stereo must not change")
	}
}

func TestTSAudioFixPassesOtherDataThrough(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("not a transport stream at all"), bytes.Repeat([]byte{0x47}, 500), append(tsHeader(), []byte("tail")...)} {
		if out := readAll(t, newTSAudioFix(bytes.NewReader(in))); !bytes.Equal(in, out) {
			t.Fatalf("data must pass through: %q", in)
		}
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// a frame that starts with a fill element (the encoder's name), as the real stream does now and then
func TestTSAudioFixSkipsFillElements(t *testing.T) {
	// raw data starts at byte 7: a fill element (id 6, count 15, escape 1 = 15 bytes) takes 3+4+8+120 = 135 bits,
	// so the next element starts at bit 7 of raw byte 16: 0 | 01.. = id 1, a channel pair
	fil := adts(6, 0xde, 360)
	fil[7+1] = 0x02
	fil[7+16], fil[7+17] = 0x00, 0x40
	for _, first := range []bool{true, false} {
		frames := [][]byte{fil, adts(6, 0x21, 350)}
		if !first { // the verdict of the stream is already known
			frames = [][]byte{adts(6, 0x21, 356), fil, adts(6, 0x21, 350)}
		}
		out := readAll(t, newTSAudioFix(bytes.NewReader(append(tsHeader(), audioPES(frames...)...))))
		for _, c := range channelConfigs(out) {
			if c != 2 {
				t.Fatalf("fill element first=%v: configs %v", first, channelConfigs(out))
			}
		}
	}
}

func TestFirstElement(t *testing.T) {
	for _, c := range []struct {
		b    []byte
		id   int
		ok   bool
		name string
	}{
		{[]byte{0x21, 0x1a}, 1, true, "channel pair"},
		{[]byte{0x01, 0x00}, 0, true, "single channel"},
		{[]byte{0x80, 0x00}, 4, true, "data stream element"},
		{[]byte{0xde}, 0, false, "fill element cut short"},
		{nil, 0, false, "empty"},
	} {
		if id, ok := firstElement(c.b); id != c.id || ok != c.ok {
			t.Errorf("%s: got %d %v", c.name, id, ok)
		}
	}
}

func TestAudioFixSettingAppliesWithoutRestart(t *testing.T) {
	ts := append(tsHeader(), audioPES(adts(6, 0x21, 356), adts(6, 0x21, 347))...)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		w.Write(ts)
	}))
	defer up.Close()
	set := testSettings(t)
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), set)
	get := func(name string) []int {
		code, body := signedGet(t, px, Payload{U: up.URL + "/" + name + ".ts"})
		if code != 200 || len(body) != len(ts) {
			t.Fatalf("%s: %d, %d bytes", name, code, len(body))
		}
		return channelConfigs([]byte(body))
	}
	if got := get("a"); !equalInts(got, []int{2, 2}) {
		t.Fatalf("on by default: %v", got)
	}
	set.Apply(map[string]any{"fix_audio_channels": "off"})
	if got := get("b"); !equalInts(got, []int{6, 6}) {
		t.Fatalf("off: %v", got)
	}
	set.Apply(map[string]any{"fix_audio_channels": "on"})
	if got := get("c"); !equalInts(got, []int{2, 2}) {
		t.Fatalf("on again: %v", got)
	}
}

// ffmpeg asks for "Range: bytes=0-" on every segment; the upstream answers 200 or 206
func TestAudioFixHandlesRangeFromStart(t *testing.T) {
	ts := append(tsHeader(), audioPES(adts(6, 0x21, 356), adts(6, 0x21, 347))...)
	var status int
	var contentRange string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp2t")
		if contentRange != "" {
			w.Header().Set("Content-Range", contentRange)
		}
		w.WriteHeader(status)
		w.Write(ts)
	}))
	defer up.Close()
	px := New(NewSigner([]byte("0123456789abcdef0123456789abcdef")), testSettings(t))
	for i, c := range []struct {
		name, rng, cr string
		code          int
		want          []int
	}{
		{"open range, answered in full", "bytes=0-", "", 200, []int{2, 2}},
		{"open range, answered as a range", "bytes=0-", "bytes 0-1000/1001", 206, []int{2, 2}},
		{"closed range from the start", "bytes=0-99999", "bytes 0-1000/1001", 206, []int{2, 2}},
		{"range from the middle", "bytes=188-", "bytes 188-1000/1001", 206, []int{6, 6}}, // not ours to touch
	} {
		status, contentRange = c.code, c.cr
		pl := Payload{U: up.URL + "/s" + string(rune('a'+i)) + ".ts", E: time.Now().Add(time.Hour).Unix()} // one URL per case: a whole file is cached now
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/x/"+px.signer.Encode(pl)+"/f", nil)
		req.Header.Set("Range", c.rng)
		px.ServeSigned(rec, req, "/x/", "http://proxy")
		body, _ := io.ReadAll(rec.Result().Body)
		if rec.Code != c.code || len(body) != len(ts) {
			t.Fatalf("%s: status %d, %d bytes", c.name, rec.Code, len(body))
		}
		if got := channelConfigs(body); !equalInts(got, c.want) {
			t.Fatalf("%s: channel configs %v, want %v", c.name, got, c.want)
		}
	}
}
