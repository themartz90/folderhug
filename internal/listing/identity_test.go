package listing

import "testing"

func TestIdentityUsesDecodedPathWithoutUnicodeNormalization(t *testing.T) {
	for _, pair := range [][2]string{{"file.txt", "%66ile.txt"}, {"a%2bb.txt", "a+b.txt"}, {"folder/a%3Fb.txt", "folder/a%3fb.txt"}} {
		if ID("profile", 1, pair[0]) != ID("profile", 1, pair[1]) {
			t.Fatalf("equivalent escapes changed identity: %q", pair)
		}
	}
	if ID("profile", 1, "caf%C3%A9.txt") == ID("profile", 1, "cafe%CC%81.txt") {
		t.Fatal("Unicode normalization merged distinct paths")
	}
	if ID("profile", 1, "a%253F.txt") == ID("profile", 1, "a%3F.txt") {
		t.Fatal("double decoding merged distinct paths")
	}
}
