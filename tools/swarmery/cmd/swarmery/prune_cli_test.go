package main

import "testing"

func TestParseRetentionDays(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{"90d", 90, false},
		{"1d", 1, false},
		{"365d", 365, false},
		{"0d", 0, true},
		{"-5d", 0, true},
		{"90", 0, true},
		{"90h", 0, true},
		{"", 0, true},
		{"abcd", 0, true},
	}
	for _, c := range cases {
		got, err := parseRetentionDays(c.in)
		if c.wantErr != (err != nil) {
			t.Errorf("parseRetentionDays(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("parseRetentionDays(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
