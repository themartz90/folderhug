package listing

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/themartz90/folderhug/internal/upstream"
)

func testRoot(t *testing.T) *upstream.Root {
	t.Helper()
	r, err := upstream.NewRoot("https://example.com/synthetic/", false)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFixtures(t *testing.T) {
	for file, format := range map[string]string{"apache-table.html": "apache_table", "apache-pre.html": "apache_pre", "nginx.html": "nginx"} {
		t.Run(format, func(t *testing.T) {
			body, err := os.Open("../../tests/fixtures/" + file)
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			root := testRoot(t)
			entries, got, err := Parse(body, root, root.URL(), "profile", 1)
			if err != nil || got != format || len(entries) != 4 {
				t.Fatalf("format=%s count=%d err=%v", got, len(entries), err)
			}
			if entries[0].Kind != "directory" || entries[0].ParentID != "root" || entries[0].Size != nil {
				t.Fatalf("directory: %+v", entries[0])
			}
			for _, e := range entries[1:] {
				if e.Size == nil || !e.SizeExact || e.TimezoneKnown || e.ModifiedAt != nil || e.ModifiedText == nil {
					t.Fatalf("metadata: %+v", e)
				}
			}
			if entries[2].Name != "100% #what?.txt" || entries[3].Name != "café & snow 雪.txt" {
				t.Fatalf("names: %+v", entries)
			}
		})
	}
}

func TestTargetsAndUncertainMetadata(t *testing.T) {
	body := `<h1>Index of /synthetic/</h1><base href="https://evil.example/"><table><tr><th>Name</th><th>Last modified</th><th>Size</th></tr>
<tr><td><a href="long%20name.txt">long...</a></td><td>2026-01-02 12:30</td><td>1.5K</td></tr>
<tr><td><a href="long%20name.txt">duplicate</a></td><td></td><td></td></tr>
<tr><td><a href="unknown.bin">unknown</a></td><td>not a time</td><td>?</td></tr>
<tr><td><a href="%C3%A9.txt">precomposed</a></td><td></td><td>-</td></tr>
<tr><td><a href="e%CC%81.txt">decomposed</a></td><td></td><td>-</td></tr>
<tr><td><a href="../">parent</a></td></tr><tr><td><a href="?C=N;O=D">sort</a></td></tr>
<tr><td><a href="https://evil.example/file">foreign</a></td></tr>
<tr><td><a href="%2e%2e/private">traversal</a></td></tr>
<tr><td><a href="%252e%252e/private">double traversal</a></td></tr>
<tr><td><a href="bad%2fseparator">separator</a></td></tr>
<tr><td><a href="nested/deep.txt">deep link</a></td></tr>
<tr><td><a href="/synthetic-other/file">prefix confusion</a></td></tr>
<tr><td><a href="javascript:alert(1)">script</a></td></tr>
<tr><td><a href="https://user:secret@example.com/synthetic/file">userinfo</a></td></tr>
<tr><td><a href="#anchor">fragment</a></td></tr></table>`
	root := testRoot(t)
	entries, _, err := Parse(strings.NewReader(body), root, root.URL(), "profile", 7)
	if err != nil || len(entries) != 4 {
		t.Fatalf("count=%d err=%v", len(entries), err)
	}
	if entries[0].Name != "long name.txt" || entries[0].Size == nil || *entries[0].Size != 1536 || entries[0].SizeExact {
		t.Fatalf("rounded metadata: %+v", entries[0])
	}
	if entries[1].Size != nil || entries[1].ModifiedAt != nil || entries[1].TimezoneKnown {
		t.Fatalf("unknown metadata: %+v", entries[1])
	}
	if entries[2].ID == entries[3].ID || entries[2].Name == entries[3].Name {
		t.Fatal("Unicode normalization changed identity")
	}
}

func TestFormatRecognition(t *testing.T) {
	root := testRoot(t)
	for _, body := range []string{
		`<html><h1>Login</h1><form><input type="password"></form></html>`,
		`<h1>Index of /synthetic/</h1><pre><a href="login">Sign in</a></pre>`,
		`<h1>Index of /synthetic/</h1><form><table><tr><th>Name</th><th>Last modified</th><th>Size</th></tr></table></form>`,
		`<a href="file.txt">file.txt</a>`,
	} {
		if _, _, err := Parse(strings.NewReader(body), root, root.URL(), "profile", 1); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("accepted non-listing: %v", err)
		}
	}
	for _, body := range []string{
		`<h1>Index of /synthetic/</h1><table><tr><th>Name</th><th>Last modified</th><th>Size</th></tr></table>`,
		`<h1>Index of /synthetic/</h1><pre>Name Last modified Size<hr><a href="../">Parent Directory</a><hr></pre>`,
		`<h1>Index of /synthetic/</h1><hr><pre><a href="../">../</a></pre><hr>`,
	} {
		entries, _, err := Parse(strings.NewReader(body), root, root.URL(), "profile", 1)
		if err != nil || len(entries) != 0 || entries == nil {
			t.Fatalf("empty listing count=%d err=%v", len(entries), err)
		}
	}
}

func TestLimitsAndIdentity(t *testing.T) {
	root := testRoot(t)
	if _, _, err := Parse(strings.NewReader(strings.Repeat("x", MaxBytes+1)), root, root.URL(), "profile", 1); !errors.Is(err, ErrLimit) {
		t.Fatalf("byte cap: %v", err)
	}
	var body strings.Builder
	body.WriteString(`<h1>Index of /synthetic/</h1><hr><pre>`)
	for i := 0; i <= MaxEntries; i++ {
		fmt.Fprintf(&body, "<a href=\"f%d.txt\">f</a> 02-Jan-2026 12:30 1\n", i)
	}
	body.WriteString(`</pre><hr>`)
	if entries, _, err := Parse(strings.NewReader(body.String()), root, root.URL(), "profile", 1); !errors.Is(err, ErrLimit) || entries != nil {
		t.Fatalf("entry cap: %v", err)
	}
	if ID("a", 1, "") != "root" || ID("a", 1, "file") == ID("a", 2, "file") || ID("a", 1, "file") == ID("b", 1, "file") {
		t.Fatal("identity scope mismatch")
	}
	if ID("a", 1, "file") != ID("a", 1, "file") {
		t.Fatal("identity is unstable")
	}
}

func TestNestedParentAndAbsoluteLinks(t *testing.T) {
	root := testRoot(t)
	current, err := root.Resolve("nested/")
	if err != nil {
		t.Fatal(err)
	}
	body := `<h1>Index of /synthetic/nested/</h1><hr><pre><a href="/synthetic/">parent</a>
<a href="https://example.com/synthetic/nested/a%26b.txt">a...</a> 02-Jan-2026 12:30 42
<a href="/synthetic/sibling/">sibling</a>
</pre><hr>`
	entries, _, err := Parse(strings.NewReader(body), root, current, "profile", 4)
	if err != nil || len(entries) != 1 {
		t.Fatalf("count=%d err=%v", len(entries), err)
	}
	if entries[0].ParentID != ID("profile", 4, "nested/") || entries[0].EscapedPath != "nested/a%26b.txt" || entries[0].Name != "a&b.txt" {
		t.Fatalf("entry: %+v", entries[0])
	}
}
