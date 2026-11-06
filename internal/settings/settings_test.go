package settings

import (
	"encoding/json"
	"testing"

	"iptv-proxy/internal/store"
)

func open(t *testing.T) (*Settings, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st), st
}

func TestDefsAreConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, d := range Defs {
		if seen[d.Key] {
			t.Errorf("duplicate key %s", d.Key)
		}
		seen[d.Key] = true
		if d.Label == "" || d.Group == "" || d.Section == "" {
			t.Errorf("%s: label, group and section are required", d.Key)
		}
		// every default must itself be valid
		if _, msg := validate(d, d.Default); msg != "" {
			t.Errorf("%s: default %v is invalid: %s", d.Key, d.Default, msg)
		}
		switch d.Kind {
		case Int:
			if _, ok := d.Default.(int); !ok {
				t.Errorf("%s: an int setting needs an int default", d.Key)
			}
		case String, URL, Enum:
			if _, ok := d.Default.(string); !ok {
				t.Errorf("%s: a text setting needs a string default", d.Key)
			}
		}
	}
}

func TestApplyValidatesStoresAndNotifies(t *testing.T) {
	s, st := open(t)
	if s.Int("health_concurrency") != 5 || s.String("log_format") != "json" {
		t.Fatal("defaults")
	}
	var changed []string
	s.OnChange(func(k []string) { changed = append(changed, k...) })

	// one bad value rejects the whole request and stores nothing
	errs := s.Apply(map[string]any{"health_concurrency": 50.0, "max_connections": 0.0, "nope": 1.0, "api_url": "ftp://x"})
	if len(errs) != 3 || errs["max_connections"] == "" || errs["nope"] == "" || errs["api_url"] == "" {
		t.Fatalf("errors: %v", errs)
	}
	if s.Int("health_concurrency") != 5 || len(changed) != 0 {
		t.Fatal("a rejected request must change nothing")
	}
	if errs := s.Apply(map[string]any{"health_concurrency": 2.5}); errs["health_concurrency"] == "" {
		t.Fatal("fractions are not whole numbers")
	}
	if errs := s.Apply(map[string]any{"log_format": "xml"}); errs["log_format"] == "" {
		t.Fatal("enum must be one of the options")
	}

	if errs := s.Apply(map[string]any{"health_concurrency": 50.0, "player_address": " http://192.168.1.10:8080/ ", "api_url": "https://mirror.example/api/"}); errs != nil {
		t.Fatal(errs)
	}
	if s.Int("health_concurrency") != 50 || s.String("player_address") != "http://192.168.1.10:8080" || s.String("api_url") != "https://mirror.example/api" {
		t.Fatalf("values not applied (address and api_url are normalised)")
	}
	if len(changed) != 3 {
		t.Fatalf("subscribers told %v", changed)
	}

	// survives a restart
	if got := New(st).Int("health_concurrency"); got != 50 {
		t.Fatalf("not persisted: %d", got)
	}

	// setting the default again removes the override and does not notify twice for no change
	changed = nil
	s.Apply(map[string]any{"health_concurrency": 50.0})
	if len(changed) != 0 {
		t.Fatal("same value must not notify")
	}
	s.Apply(map[string]any{"health_concurrency": 5.0})
	if st.Meta(prefix+"health_concurrency") != "" || New(st).Int("health_concurrency") != 5 {
		t.Fatal("default restored, override removed")
	}
}

func TestBlankRules(t *testing.T) {
	s, _ := open(t)
	if errs := s.Apply(map[string]any{"player_address": ""}); errs != nil {
		t.Fatalf("player_address may be empty: %v", errs)
	}
	if errs := s.Apply(map[string]any{"api_url": ""}); errs["api_url"] == "" {
		t.Fatal("api_url may not be empty")
	}
	if errs := s.Apply(map[string]any{"ua_vlc": "   "}); errs["ua_vlc"] == "" {
		t.Fatal("user agents may not be empty")
	}
}

func TestInvalidStoredValueFallsBackToDefault(t *testing.T) {
	_, st := open(t)
	st.SetMeta(prefix+"max_connections", "-5")
	st.SetMeta(prefix+"log_format", `"yaml"`)
	s := New(st)
	if s.Int("max_connections") != 20 || s.String("log_format") != "json" {
		t.Fatal("invalid stored values must be ignored")
	}
}

func TestAllListsEverySettingWithItsValue(t *testing.T) {
	s, _ := open(t)
	s.Apply(map[string]any{"session_days": 30.0})
	all := s.All()
	if len(all) != len(Defs) {
		t.Fatal("count")
	}
	for _, v := range all {
		if v.Key == "session_days" && v.Value != 30 {
			t.Fatalf("value %v", v.Value)
		}
	}
}

func TestMinAndMaxAreAlwaysInTheJSON(t *testing.T) {
	// a minimum of 0 must not be dropped: the Settings page reads it for every number
	s, _ := open(t)
	b, err := json.Marshal(s.All())
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	json.Unmarshal(b, &rows)
	for _, r := range rows {
		if _, ok := r["min"]; !ok {
			t.Errorf("%v: min missing", r["key"])
		}
		if _, ok := r["max"]; !ok {
			t.Errorf("%v: max missing", r["key"])
		}
	}
}

func TestTimeAndZoneValues(t *testing.T) {
	s, _ := open(t)
	if errs := s.Apply(map[string]any{"health_quiet_start": "23:30", "health_quiet_end": "06:00", "time_zone": "Europe/Vilnius"}); errs != nil {
		t.Fatal(errs)
	}
	if s.String("health_quiet_start") != "23:30" || s.String("time_zone") != "Europe/Vilnius" {
		t.Fatal("not applied")
	}
	for _, bad := range []map[string]any{{"health_quiet_start": "25:00"}, {"health_quiet_start": "7pm"}, {"time_zone": "Mars/Base"}} {
		if errs := s.Apply(bad); len(errs) != 1 {
			t.Fatalf("%v should be rejected: %v", bad, errs)
		}
	}
	if errs := s.Apply(map[string]any{"health_quiet_start": "", "time_zone": ""}); errs != nil {
		t.Fatalf("both may be cleared: %v", errs)
	}
}
