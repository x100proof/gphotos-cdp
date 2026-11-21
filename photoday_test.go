package main

import (
	"testing"
	"time"
)

func TestFromToUsePhotoOwnDate(t *testing.T) {
	defer func(f, to time.Time) { fromDate, toDate = f, to }(fromDate, toDate)
	fromDate, _ = time.Parse(time.DateOnly, "2026-10-01")
	toDate, _ = time.Parse(time.DateOnly, "2026-10-31")
	zone := func(h int) *time.Location { return time.FixedZone("", h*3600) }

	cases := []struct {
		name                string
		taken               time.Time
		beforeFrom, afterTo bool
	}{
		{"Brisbane 1 Oct 08:27 (was skipped when -from meant UTC midnight)", time.Date(2026, 10, 1, 8, 27, 0, 0, zone(10)), false, false},
		{"Brisbane 30 Sep 23:59", time.Date(2026, 9, 30, 23, 59, 0, 0, zone(10)), true, false},
		{"London 1 Oct 07:00 (30 Sep 21:00 in Brisbane)", time.Date(2026, 10, 1, 7, 0, 0, 0, zone(1)), false, false},
		{"Honolulu 30 Sep 22:00 (1 Oct in Brisbane)", time.Date(2026, 9, 30, 22, 0, 0, 0, zone(-10)), true, false},
		{"Brisbane 31 Oct 23:59", time.Date(2026, 10, 31, 23, 59, 0, 0, zone(10)), false, false},
		{"Auckland 1 Nov 00:30 (31 Oct in Brisbane)", time.Date(2026, 11, 1, 0, 30, 0, 0, zone(13)), false, true},
	}
	for _, c := range cases {
		if got := beforeFromDate(c.taken); got != c.beforeFrom {
			t.Errorf("%s: beforeFromDate = %v, want %v", c.name, got, c.beforeFrom)
		}
		if got := afterToDate(c.taken); got != c.afterTo {
			t.Errorf("%s: afterToDate = %v, want %v", c.name, got, c.afterTo)
		}
	}
}

func TestNoFromToNeverFilters(t *testing.T) {
	defer func(f, to time.Time) { fromDate, toDate = f, to }(fromDate, toDate)
	fromDate, toDate = time.Time{}, time.Time{}
	now := time.Now()
	if beforeFromDate(now) || afterToDate(now) {
		t.Fatal("with no -from/-to nothing should be filtered")
	}
}
