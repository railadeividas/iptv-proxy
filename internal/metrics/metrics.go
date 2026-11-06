// Package metrics writes metrics in the Prometheus text exposition format (version 0.0.4). It only formats: the
// numbers come from the rest of the app (see internal/server/metrics.go). No dependency is needed for this.
package metrics

import (
	"io"
	"math"
	"strconv"
	"strings"
)

const ContentType = "text/plain; version=0.0.4; charset=utf-8"

type sample struct {
	labels string
	value  float64
}

type family struct {
	name, help, kind string
	samples          []sample
}

// Set collects samples, groups them by metric (the format wants every line of a metric together) and writes them.
type Set struct {
	order []string
	fams  map[string]*family
}

func New() *Set { return &Set{fams: map[string]*family{}} }

func (s *Set) add(kind, name, help string, v float64, labels []string) {
	f := s.fams[name]
	if f == nil {
		f = &family{name: name, help: help, kind: kind}
		s.fams[name] = f
		s.order = append(s.order, name)
	}
	f.samples = append(f.samples, sample{labels: formatLabels(labels), value: v})
}

// Gauge adds a value that can go up and down. labels are name, value pairs.
func (s *Set) Gauge(name, help string, v float64, labels ...string) {
	s.add("gauge", name, help, v, labels)
}

// Counter adds a value that only grows (until the app restarts). By convention its name ends in _total.
func (s *Set) Counter(name, help string, v float64, labels ...string) {
	s.add("counter", name, help, v, labels)
}

// Bool adds a gauge of 1 or 0.
func (s *Set) Bool(name, help string, v bool, labels ...string) {
	n := 0.0
	if v {
		n = 1
	}
	s.add("gauge", name, help, n, labels)
}

func formatLabels(kv []string) string {
	if len(kv) < 2 {
		return ""
	}
	var b strings.Builder
	b.WriteByte('{')
	for i := 0; i+1 < len(kv); i += 2 {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(kv[i])
		b.WriteString(`="`)
		b.WriteString(escape(kv[i+1]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escape(v string) string { return labelEscaper.Replace(strings.ToValidUTF8(v, "")) }

var helpEscaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

// WriteTo writes everything collected.
func (s *Set) WriteTo(w io.Writer) (int64, error) {
	var b strings.Builder
	for _, name := range s.order {
		f := s.fams[name]
		b.WriteString("# HELP " + f.name + " " + helpEscaper.Replace(f.help) + "\n")
		b.WriteString("# TYPE " + f.name + " " + f.kind + "\n")
		for _, x := range f.samples {
			b.WriteString(f.name + x.labels + " " + formatValue(x.value) + "\n")
		}
	}
	n, err := io.WriteString(w, b.String())
	return int64(n), err
}
