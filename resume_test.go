package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLabelTime(t *testing.T) {
	cases := map[string]string{
		"Photo - Landscape - Apr 29, 2026, 10:00:00 AM": "2026-04-29T10:00:00",
		"Video - Portrait - Nov 23, 2026, 6:58:45 PM":   "2026-11-23T18:58:45",
		"Photo - Portrait - Jan 1, 2020, 12:05:09 AM":   "2020-01-01T00:05:09",
		"Photo - Square - Dec 31, 2019, 12:59:59 PM":    "2019-12-31T12:59:59",
		"Photo – Landscape – 25 Nov 2026, 00:00:06":     "2026-11-25T00:00:06",
		"Video – Portrait – 3 Sept 2025, 23:59:01":      "2025-09-03T23:59:01",
		"Photo – Square – 7 Jun 2011, 9:05":             "2011-06-07T09:05:00",
		"Photo – Square – 7 Jun 2011, 9:05:00 pm":       "2011-06-07T21:05:00",
	}
	for label, want := range cases {
		got, ok := labelTime(label)
		if !ok || got.Format(resumeTimeLayout) != want {
			t.Errorf("labelTime(%q) = %v, %v; want %s", label, got, ok, want)
		}
	}
	for _, bad := range []string{"", "Photo", "Photo - Apr 29, 2026", "Apr 29, 2026, 13:00:00 PM", "Photo – 29 Apr 2026", "29 Apr 2026, 24:00:00"} {
		if _, ok := labelTime(bad); ok {
			t.Errorf("labelTime(%q) parsed, want failure", bad)
		}
	}
}

func TestResumeAndIdFiles(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "resume")
	if at, _, _, err := readResume(p); err != nil || at != "" {
		t.Fatalf("missing file: %q %v", at, err)
	}
	ts := time.Date(2026, 4, 29, 10, 0, 0, 0, time.UTC).Format(resumeTimeLayout)
	if err := writeFileAtomic(p, ts+"\tAF1QipX\n"); err != nil {
		t.Fatal(err)
	}
	if at, id, wide, err := readResume(p); err != nil || at != ts || id != "AF1QipX" || wide {
		t.Fatalf("got %q %q %v %v", at, id, wide, err)
	}
	os.WriteFile(p, []byte(ts+"\tAF1QipX\twide\n"), 0644)
	if at, id, wide, err := readResume(p); err != nil || at != ts || id != "AF1QipX" || !wide {
		t.Fatalf("wide: got %q %q %v %v", at, id, wide, err)
	}
	os.WriteFile(p, []byte("end\n"), 0644)
	if at, _, _, _ := readResume(p); at != "end" {
		t.Fatalf("end: got %q", at)
	}
	os.WriteFile(p, []byte("garbage\n"), 0644)
	if _, _, _, err := readResume(p); err == nil {
		t.Fatal("garbage accepted")
	}
	ids := filepath.Join(dir, "ids")
	os.WriteFile(ids, []byte("a\n\nb\t3\na\n../x\n"), 0644)
	got, err := readIdList(ids)
	if err != nil || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("readIdList = %v %v", got, err)
	}
	os.MkdirAll(filepath.Join(dir, "dl", "p1"), 0755)
	os.WriteFile(filepath.Join(dir, "dl", "p1", "f.jpg"), nil, 0644)
	if has, _ := hasFileOnDisk(filepath.Join(dir, "dl"), "p1"); has {
		t.Fatal("empty file counted")
	}
	os.Truncate(filepath.Join(dir, "dl", "p1", "f.jpg"), 100) // sparse placeholder counts
	if has, _ := hasFileOnDisk(filepath.Join(dir, "dl"), "p1"); !has {
		t.Fatal("placeholder not counted")
	}
}
