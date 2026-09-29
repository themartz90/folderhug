package upstream

import (
	"net/url"
	"testing"
)

func TestRootRejectsUnsafeURLs(t *testing.T) {
	for _, raw := range []string{
		"http://example.com/files/", "ftp://example.com/files/", "https://user:password@example.com/files/",
		"https://example.com/files/?token=secret", "https://example.com/files/#fragment", "https:example.com/files/",
		"https://example.com/files/../secret", "https://example.com/files/%2e%2e/secret", "https://example.com/files/%252e%252e/secret",
		"https://example.com/files/%2fsecret", "https://example.com/files/%255csecret", "https://example.com/files/%00name",
		"https://example.com/files/\\secret", "https://[fe80::1%25eth0]/files/", "https://example.com:0/files/",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := NewRoot(raw, false); err == nil {
				t.Fatal("unsafe root accepted")
			}
		})
	}
}

func TestRelativePathsPreserveEncoding(t *testing.T) {
	root, err := NewRoot("https://EXAMPLE.com:443/files/", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", "nested/", "100%25%20%23what%3F.txt", "caf%C3%A9%20%26%20snow%20%E9%9B%AA.txt", "literal%2520name.txt"} {
		u, err := root.Resolve(path)
		if err != nil {
			t.Fatalf("%q: %v", path, err)
		}
		if u.String() != "https://example.com/files/"+path {
			t.Fatalf("encoding changed: %s", u)
		}
		relative, err := root.Relative(u)
		if err != nil || relative != path {
			t.Fatalf("relative=%q err=%v", relative, err)
		}
	}
	copy := root.URL()
	copy.Host = "evil.example"
	if root.URL().Host != "example.com" {
		t.Fatal("root was mutable")
	}
}

func TestChildConfinement(t *testing.T) {
	root, _ := NewRoot("https://example.com/files", false)
	canonical, err := root.Child(root.URL(), "/files/")
	if err != nil {
		t.Fatal(err)
	}
	for _, href := range []string{"nested/file.txt", "/files/one.txt", "https://example.com:443/files/two.txt"} {
		if _, err := root.Child(canonical, href); err != nil {
			t.Errorf("safe child %q: %v", href, err)
		}
	}
	for _, href := range []string{
		"../", "./file", "a/../file", "%2e%2e/file", "%252e%252e/file", "%25252e%25252e/file",
		"%2foutside", "%5coutside", "folder%2Ffile", "https://other.example/files/x", "//other.example/files/x",
		"http://example.com/files/x", "https://user:pass@example.com/files/x", "/files-other/x", "/file/x",
		"?C=N;O=D", "file?token=x", "file#", "file%00.txt", "file%0a.txt", "\\evil", "a//b", "%invalid",
	} {
		t.Run(href, func(t *testing.T) {
			if _, err := root.Child(canonical, href); err == nil {
				t.Fatal("unsafe child accepted")
			}
		})
	}
	outside, _ := url.Parse("https://example.com/outside/")
	if _, err := root.Child(outside, "/files/valid.txt"); err == nil {
		t.Fatal("unvalidated current URL accepted")
	}
	for _, path := range []string{"/absolute", "https://example.com/files/x", "../x", "x?query", "x#fragment"} {
		if _, err := root.Resolve(path); err == nil {
			t.Errorf("unsafe relative path accepted: %q", path)
		}
	}
}
