package playlist

import (
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	in := "#EXTM3U\n#EXTINF:-1 tvg-id=\"a.us\" tvg-logo=\"http://l/x.png\" group-title=\"News\",Chan A\n#EXTVLCOPT:http-referrer=http://r/\n#EXTVLCOPT:http-user-agent=UA1\nhttp://h/a.m3u8\n#EXTINF:-1,Bad\nrtmp://h/x\n#EXTINF:-1,Kodi\nhttp://h/k.m3u8|User-Agent=KUA&Referer=http://kr/\n"
	items, skipped, err := Parse(strings.NewReader(in))
	if err != nil || len(items) != 2 || len(skipped) != 1 {
		t.Fatalf("%v %+v %+v", err, items, skipped)
	}
	if items[0].Referrer != "http://r/" || items[0].UserAgent != "UA1" || items[0].Group != "News" || items[0].TvgID != "a.us" {
		t.Fatalf("%+v", items[0])
	}
	if items[1].UserAgent != "KUA" || items[1].URL != "http://h/k.m3u8" {
		t.Fatalf("%+v", items[1])
	}
	if _, _, err := Parse(strings.NewReader("<html>nope</html>")); err == nil {
		t.Fatal("expected error for non-M3U")
	}
}
