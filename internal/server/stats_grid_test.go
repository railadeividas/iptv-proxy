package server

import (
	"testing"
	"time"
)

func TestBucketGridAlignment(t *testing.T) {
	// Wednesday 2030-01-09 10:07:30 UTC to Friday 2030-01-11 08:00:00 UTC
	from := time.Date(2030, 1, 9, 10, 7, 30, 0, time.UTC).Unix()
	to := time.Date(2030, 1, 11, 8, 0, 0, 0, time.UTC).Unix()
	for _, tz := range []int64{0, 3600, -5 * 3600, 19800, 20700, -34200} {
		for _, step := range bucketSteps {
			g := bucketGrid(from, to, step, tz)
			off := tz
			if step == weekStep {
				off = tz - 345600
			}
			if (g.Start+off)%step != 0 || (g.End+off)%step != 0 {
				t.Fatalf("tz %d step %d: boundaries are not aligned: %+v", tz, step, g)
			}
			if g.Start > from || g.Start <= from-step || g.End < to || g.End >= to+step || g.End-g.Start != int64(g.N)*step || g.Bucket != step || g.From != from || g.To != to {
				t.Fatalf("tz %d step %d: the grid must widen the range to whole buckets, by less than one: %+v", tz, step, g)
			}
		}
	}
	// a boundary stays where it is
	b := time.Date(2030, 1, 9, 0, 0, 0, 0, time.UTC).Unix()
	if g := bucketGrid(b, b+86400, 86400, 0); g.Start != b || g.End != b+86400 || g.N != 1 {
		t.Fatalf("on the grid: %+v", g)
	}
	// weeks start on Monday in the viewer's zone: Monday 2030-01-07 00:00 UTC, and 05:30 earlier east of UTC
	mon := time.Date(2030, 1, 7, 0, 0, 0, 0, time.UTC).Unix()
	if g := bucketGrid(from, to, weekStep, 0); g.Start != mon || g.N != 1 {
		t.Fatalf("a UTC week: %+v", g)
	}
	if g := bucketGrid(from, to, weekStep, 19800); g.Start != mon-19800 {
		t.Fatalf("a week at UTC+5:30: %+v", g)
	}
	// the same instant in UTC-5 belongs to the week that started Monday 05:00 UTC
	if g := bucketGrid(mon+3600, mon+7200, weekStep, -5*3600); g.Start != mon-7*86400+5*3600 || g.N != 1 {
		t.Fatalf("a week at UTC-5: %+v", g)
	}
}
