// Package settings holds every tunable of the app. Values live in the
// database, are validated against Defs and take effect immediately: components
// read them when they need them or subscribe with OnChange.
package settings

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/url"
	"strings"
	"sync"
	"time"

	"iptv-proxy/internal/store"
)

const prefix = "setting."

type Settings struct {
	st   *store.Store
	defs map[string]Def

	mu   sync.RWMutex
	vals map[string]any // only overridden values; others use the default

	subMu sync.Mutex
	subs  []func(changed []string)
}

// New loads the stored values. Invalid stored values are ignored (defaults apply).
func New(st *store.Store) *Settings {
	s := &Settings{st: st, defs: map[string]Def{}, vals: map[string]any{}}
	for _, d := range Defs {
		s.defs[d.Key] = d
		raw := st.Meta(prefix + d.Key)
		if raw == "" {
			continue
		}
		var v any
		if err := json.Unmarshal([]byte(raw), &v); err == nil {
			if nv, msg := validate(d, v); msg == "" {
				s.vals[d.Key] = nv
				continue
			}
		}
		slog.Warn("ignoring invalid stored setting", "key", d.Key)
	}
	return s
}

func (s *Settings) get(key string) any {
	d, ok := s.defs[key]
	if !ok {
		panic("unknown setting " + key) // a typo in the code, never user input
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if v, ok := s.vals[key]; ok {
		return v
	}
	return d.Default
}

func (s *Settings) Int(key string) int       { return s.get(key).(int) }
func (s *Settings) String(key string) string { return s.get(key).(string) }

func (s *Settings) Hours(key string) time.Duration   { return time.Duration(s.Int(key)) * time.Hour }
func (s *Settings) Minutes(key string) time.Duration { return time.Duration(s.Int(key)) * time.Minute }
func (s *Settings) Seconds(key string) time.Duration { return time.Duration(s.Int(key)) * time.Second }
func (s *Settings) MB(key string) int64              { return int64(s.Int(key)) << 20 }

// Value is one setting with its current value, for the Settings pages.
type Value struct {
	Def
	Value any `json:"value"`
}

func (s *Settings) All() []Value {
	out := make([]Value, 0, len(Defs))
	for _, d := range Defs {
		out = append(out, Value{Def: d, Value: s.get(d.Key)})
	}
	return out
}

// OnChange registers a callback that receives the keys that changed.
func (s *Settings) OnChange(fn func(changed []string)) {
	s.subMu.Lock()
	s.subs = append(s.subs, fn)
	s.subMu.Unlock()
}

// Apply validates and stores values (key -> value). Nothing is stored if any
// value is invalid; the returned map then holds a message per bad key.
func (s *Settings) Apply(in map[string]any) map[string]string {
	errs := map[string]string{}
	clean := map[string]any{}
	for k, v := range in {
		d, ok := s.defs[k]
		if !ok {
			errs[k] = "unknown setting"
			continue
		}
		nv, msg := validate(d, v)
		if msg != "" {
			errs[k] = msg
			continue
		}
		clean[k] = nv
	}
	if len(errs) > 0 {
		return errs
	}
	var changed []string
	s.mu.Lock()
	for k, nv := range clean {
		d := s.defs[k]
		cur, overridden := s.vals[k]
		if !overridden {
			cur = d.Default
		}
		if cur == nv {
			continue
		}
		changed = append(changed, k)
		if nv == d.Default {
			delete(s.vals, k)
			s.st.DeleteMeta(prefix + k)
		} else {
			s.vals[k] = nv
			b, _ := json.Marshal(nv)
			s.st.SetMeta(prefix+k, string(b))
		}
	}
	s.mu.Unlock()
	if len(changed) > 0 {
		s.subMu.Lock()
		subs := append([]func([]string){}, s.subs...)
		s.subMu.Unlock()
		for _, fn := range subs {
			fn(changed)
		}
		s.st.Notify2("settings")
	}
	return nil
}

// validate checks one value and returns it normalised (int / string).
func validate(d Def, v any) (any, string) {
	switch d.Kind {
	case Int:
		f, ok := v.(float64)
		if !ok {
			if i, isInt := v.(int); isInt {
				f, ok = float64(i), true
			}
		}
		if !ok || math.IsNaN(f) || f != math.Trunc(f) {
			return nil, "enter a whole number"
		}
		n := int(f)
		if n < d.Min || n > d.Max {
			return nil, fmt.Sprintf("must be between %d and %d", d.Min, d.Max)
		}
		return n, ""
	case Enum:
		str, _ := v.(string)
		for _, o := range d.Options {
			if o.Value == str {
				return str, ""
			}
		}
		return nil, "choose one of the listed options"
	case Time:
		str, _ := v.(string)
		str = strings.TrimSpace(str)
		if str == "" {
			if d.Blank {
				return "", ""
			}
			return nil, "cannot be empty"
		}
		if _, err := time.Parse("15:04", str); err != nil {
			return nil, "enter a time like 23:30"
		}
		return str, ""
	case Zone:
		str, _ := v.(string)
		str = strings.TrimSpace(str)
		if str == "" {
			if d.Blank {
				return "", ""
			}
			return nil, "cannot be empty"
		}
		if _, err := time.LoadLocation(str); err != nil {
			return nil, "unknown time zone, use a name like Europe/Vilnius"
		}
		return str, ""
	case String, URL:
		str, ok := v.(string)
		if !ok {
			return nil, "enter text"
		}
		str = strings.TrimSpace(str)
		if d.Kind == URL {
			str = strings.TrimRight(str, "/")
		}
		if str == "" {
			if d.Blank {
				return "", ""
			}
			return nil, "cannot be empty"
		}
		if len(str) > 500 {
			return nil, "too long"
		}
		if d.Kind == URL {
			if u, err := url.Parse(str); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return nil, "must look like http://192.168.1.10:8810"
			}
		}
		return str, ""
	}
	return nil, "unsupported setting type"
}
