package epg

import (
	"bytes"
	"strings"
	"testing"
)

func TestFilterXMLTV(t *testing.T) {
	in := `<?xml version="1.0" encoding="UTF-8"?>
<tv generator-info-name="x"><channel id="A.us"><display-name>A &amp; B</display-name></channel><channel id="B.us"><display-name>B</display-name></channel>
<programme start="1" stop="2" channel="A.us"><title lang="en">Show</title></programme><programme start="1" stop="2" channel="B.us"><title>Other</title></programme></tv>`
	var out bytes.Buffer
	if err := filterXMLTV(strings.NewReader(in), &out, map[string]bool{"A.us": true}); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "A &amp; B") || !strings.Contains(s, "Show") || strings.Contains(s, "B.us") || strings.Contains(s, "Other") {
		t.Fatalf("bad filter:\n%s", s)
	}
}
