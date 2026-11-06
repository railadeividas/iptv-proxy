package metrics

import (
	"math"
	"strings"
	"testing"
)

func render(s *Set) string {
	var b strings.Builder
	s.WriteTo(&b)
	return b.String()
}

func TestFormat(t *testing.T) {
	s := New()
	s.Gauge("app_up", "Is it up.", 1)
	s.Counter("app_requests_total", "Requests.", 7, "route", "api", "code", "200")
	s.Gauge("app_up", "Is it up.", 2, "zone", "a") // same metric later: still written together
	s.Counter("app_requests_total", "Requests.", 3, "route", "ui", "code", "404")
	s.Bool("app_flag", "A flag.", true)
	s.Gauge("app_weird", "Odd values\nand a backslash \\", math.Inf(1))
	s.Gauge("app_nan", "Not a number.", math.NaN())
	want := `# HELP app_up Is it up.
# TYPE app_up gauge
app_up 1
app_up{zone="a"} 2
# HELP app_requests_total Requests.
# TYPE app_requests_total counter
app_requests_total{route="api",code="200"} 7
app_requests_total{route="ui",code="404"} 3
# HELP app_flag A flag.
# TYPE app_flag gauge
app_flag 1
# HELP app_weird Odd values\nand a backslash \\
# TYPE app_weird gauge
app_weird +Inf
# HELP app_nan Not a number.
# TYPE app_nan gauge
app_nan NaN
`
	if got := render(s); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestLabelValuesAreEscaped(t *testing.T) {
	s := New()
	s.Gauge("x", "h", 1, "profile", "Living \"room\"\nTV \\ 1", "bad", "a\xffb")
	got := render(s)
	if !strings.Contains(got, `x{profile="Living \"room\"\nTV \\ 1",bad="ab"} 1`) {
		t.Fatalf("escaping: %s", got)
	}
}
