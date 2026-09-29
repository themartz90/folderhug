package testresource

import "testing"

func TestParseRSS(t *testing.T) {
	got, err := parseRSS("Name:\tfixture\nVmSize:\t999999 kB\nVmRSS:\t12345 kB\nVmSwap:\t678 kB\n")
	if err != nil || got != 12345*1024 {
		t.Fatalf("RSS=%d err=%v", got, err)
	}
	for _, value := range []string{"VmSize: 123 kB", "VmRSS: 1 MB", "VmRSS: -1 kB", "VmRSS: invalid kB", "VmRSS: 18446744073709551615 kB", "VmRSS: 12"} {
		if _, err := parseRSS(value); err == nil {
			t.Errorf("accepted invalid RSS status %q", value)
		}
	}
}
