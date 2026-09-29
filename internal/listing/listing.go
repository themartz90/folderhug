package listing

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/themartz90/folderhug/internal/upstream"
	"golang.org/x/net/html"
)

const (
	MaxBytes   = 8 << 20
	MaxEntries = 50000
)

var (
	ErrUnsupported = errors.New("unsupported directory listing")
	ErrLimit       = errors.New("directory listing limit exceeded")
)

type Entry struct {
	ID            string     `json:"id"`
	ProfileID     string     `json:"profile_id"`
	ParentID      string     `json:"parent_id"`
	Name          string     `json:"name"`
	EscapedPath   string     `json:"escaped_path"`
	Kind          string     `json:"kind"`
	Extension     *string    `json:"extension"`
	Category      string     `json:"category"`
	Size          *int64     `json:"size"`
	SizeExact     bool       `json:"size_exact"`
	ModifiedAt    *time.Time `json:"modified_at"`
	ModifiedText  *string    `json:"modified_text"`
	TimezoneKnown bool       `json:"timezone_known"`
	Provenance    string     `json:"provenance"`
}

func ID(profileID string, generation int, escapedPath string) string {
	if escapedPath == "" {
		return "root"
	}
	identityPath, err := url.PathUnescape(escapedPath)
	if err != nil {
		identityPath = escapedPath
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%s:%d:%s", len(profileID), profileID, generation, identityPath)))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// Parse returns a complete supported listing or an error, never truncated success.
func Parse(reader io.Reader, root *upstream.Root, current *url.URL, profileID string, generation int) ([]Entry, string, error) {
	if root == nil || current == nil {
		return nil, "", upstream.ErrURL
	}
	currentPath, err := root.Relative(current)
	if err != nil {
		return nil, "", err
	}
	body, err := io.ReadAll(io.LimitReader(reader, MaxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > MaxBytes {
		return nil, "", ErrLimit
	}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil, "", ErrUnsupported
	}
	var heading bool
	var forms bool
	var tables, pres []*html.Node
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		switch n.Data {
		case "h1":
			heading = heading || strings.HasPrefix(strings.ToLower(strings.TrimSpace(text(n))), "index of /")
		case "form", "input":
			forms = true
		case "table":
			tables = append(tables, n)
		case "pre":
			pres = append(pres, n)
		}
	})
	if !heading || forms {
		return nil, "", ErrUnsupported
	}
	p := parser{root: root, current: current, profileID: profileID, generation: generation, parentID: ID(profileID, generation, currentPath), entries: []Entry{}, seen: make(map[string]bool)}
	for _, table := range tables {
		columns, ok := tableColumns(table)
		if !ok {
			continue
		}
		p.format = "apache_table"
		if err := p.table(table, columns); err != nil {
			return nil, "", err
		}
		return p.entries, p.format, nil
	}
	for _, pre := range pres {
		header := strings.ToLower(strings.Join(strings.Fields(preHeader(pre)), " "))
		if strings.Contains(header, "name") && strings.Contains(header, "last modified") && strings.Contains(header, "size") {
			p.format = "apache_pre"
		} else if hasSiblingHR(pre, true) && hasSiblingHR(pre, false) {
			p.format = "nginx"
		} else {
			continue
		}
		if err := p.pre(pre); err != nil {
			return nil, "", err
		}
		return p.entries, p.format, nil
	}
	return nil, "", ErrUnsupported
}

type parser struct {
	root       *upstream.Root
	current    *url.URL
	profileID  string
	generation int
	parentID   string
	format     string
	entries    []Entry
	seen       map[string]bool
}

func (p *parser) add(anchor *html.Node, modified, size string) error {
	href, ok := attribute(anchor, "href")
	if !ok || href == "" || strings.TrimSpace(text(anchor)) == "" {
		return nil
	}
	target, err := p.root.Child(p.current, href)
	if err != nil {
		return nil
	}
	escaped, err := p.root.Relative(target)
	if err != nil || escaped == "" {
		return nil
	}
	// Listings describe direct children. Ancestors, siblings and deep shortcuts
	// must not acquire the current directory as their parent.
	currentPrefix := strings.TrimSuffix(p.current.Path, "/") + "/"
	if !strings.HasPrefix(target.Path, currentPrefix) {
		return nil
	}
	child := strings.TrimPrefix(target.Path, currentPrefix)
	if child == "" || strings.Contains(strings.TrimSuffix(child, "/"), "/") {
		return nil
	}
	if p.seen[target.Path] {
		return nil
	}
	if len(p.entries) >= MaxEntries {
		return ErrLimit
	}
	p.seen[target.Path] = true
	name, err := url.PathUnescape(path.Base(strings.TrimSuffix(target.EscapedPath(), "/")))
	if err != nil {
		return nil
	}
	e := Entry{ID: ID(p.profileID, p.generation, escaped), ProfileID: p.profileID, ParentID: p.parentID, Name: name, EscapedPath: escaped, Kind: "file", Category: "other", Provenance: p.format}
	if strings.HasSuffix(target.Path, "/") {
		e.Kind = "directory"
		e.Category = "folder"
	} else {
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
		if ext != "" {
			e.Extension = &ext
			e.Category = category(ext)
		}
		e.Size, e.SizeExact = parseSize(size)
	}
	modified = strings.TrimSpace(modified)
	if modified != "" && modified != "-" {
		e.ModifiedText = &modified
		if instant, err := time.Parse(time.RFC3339, modified); err == nil {
			e.ModifiedAt = &instant
			e.TimezoneKnown = true
		}
	}
	p.entries = append(p.entries, e)
	return nil
}

type columns struct{ name, modified, size int }

func tableColumns(table *html.Node) (columns, bool) {
	result := columns{-1, -1, -1}
	for _, row := range rows(table) {
		for i, cell := range cells(row) {
			value := strings.ToLower(strings.Join(strings.Fields(text(cell)), " "))
			switch value {
			case "name":
				result.name = i
			case "last modified":
				result.modified = i
			case "size":
				result.size = i
			}
		}
		if result.name >= 0 && result.modified >= 0 && result.size >= 0 {
			return result, true
		}
		result = columns{-1, -1, -1}
	}
	return result, false
}

func (p *parser) table(table *html.Node, c columns) error {
	for _, row := range rows(table) {
		fields := cells(row)
		if c.name >= len(fields) {
			continue
		}
		modified, size := "", ""
		if c.modified < len(fields) {
			modified = text(fields[c.modified])
		}
		if c.size < len(fields) {
			size = text(fields[c.size])
		}
		for _, anchor := range anchors(fields[c.name]) {
			if err := p.add(anchor, modified, size); err != nil {
				return err
			}
		}
	}
	return nil
}

func (p *parser) pre(pre *html.Node) error {
	for _, anchor := range anchors(pre) {
		var tail strings.Builder
		for next := anchor.NextSibling; next != nil; next = next.NextSibling {
			if next.Type == html.ElementNode && (next.Data == "a" || next.Data == "hr" || next.Data == "br") {
				break
			}
			value := text(next)
			if before, _, found := strings.Cut(value, "\n"); found {
				tail.WriteString(before)
				break
			}
			tail.WriteString(value)
		}
		modified, size := preMetadata(tail.String())
		if err := p.add(anchor, modified, size); err != nil {
			return err
		}
	}
	return nil
}

func preMetadata(raw string) (string, string) {
	fields := strings.Fields(raw)
	if len(fields) == 0 {
		return "", ""
	}
	for _, layout := range []string{"02-Jan-2006 15:04", "2006-01-02 15:04", "02-Jan-2006 15:04:05", "2006-01-02 15:04:05"} {
		if len(fields) < 2 {
			break
		}
		date := fields[0] + " " + fields[1]
		if _, err := time.Parse(layout, date); err == nil {
			if len(fields) > 2 {
				return date, fields[2]
			}
			return date, ""
		}
	}
	if _, err := time.Parse(time.RFC3339, fields[0]); err == nil {
		if len(fields) > 1 {
			return fields[0], fields[1]
		}
		return fields[0], ""
	}
	if len(fields) == 1 {
		return "", fields[0]
	}
	return "", ""
}

func parseSize(raw string) (*int64, bool) {
	value := strings.TrimSpace(raw)
	if n, err := strconv.ParseInt(value, 10, 64); err == nil && n >= 0 {
		return &n, true
	}
	if len(value) < 2 {
		return nil, false
	}
	unit := strings.ToUpper(value[len(value)-1:])
	power := strings.Index("KMGTPE", unit) + 1
	if power == 0 {
		return nil, false
	}
	number, err := strconv.ParseFloat(strings.TrimSpace(value[:len(value)-1]), 64)
	bytes := number * math.Pow(1024, float64(power))
	if err != nil || math.IsNaN(bytes) || math.IsInf(bytes, 0) || bytes < 0 || bytes >= float64(math.MaxInt64) {
		return nil, false
	}
	n := int64(bytes)
	return &n, false
}

func category(extension string) string {
	for cat, extensions := range map[string]string{
		"video":    " mp4 mkv webm mov avi m4v mpg mpeg ts ",
		"image":    " jpg jpeg png gif webp avif bmp tif tiff heic svg ",
		"audio":    " mp3 m4a aac flac ogg opus wav ",
		"document": " txt md pdf doc docx odt rtf csv xls xlsx ppt pptx ",
		"archive":  " zip rar 7z tar gz bz2 xz zst ",
	} {
		if strings.Contains(extensions, " "+extension+" ") {
			return cat
		}
	}
	return "other"
}

func walk(root *html.Node, visit func(*html.Node)) {
	for node := root; node != nil; {
		visit(node)
		if node.FirstChild != nil {
			node = node.FirstChild
			continue
		}
		for node != root && node.NextSibling == nil {
			node = node.Parent
		}
		if node == root {
			return
		}
		node = node.NextSibling
	}
}

func text(node *html.Node) string {
	var value strings.Builder
	walk(node, func(n *html.Node) {
		if n.Type == html.TextNode {
			value.WriteString(n.Data)
		}
	})
	return value.String()
}

func attribute(node *html.Node, key string) (string, bool) {
	for _, a := range node.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func anchors(root *html.Node) []*html.Node {
	var found []*html.Node
	walk(root, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			found = append(found, n)
		}
	})
	return found
}

func rows(table *html.Node) []*html.Node {
	var found []*html.Node
	walk(table, func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "tr" {
			found = append(found, n)
		}
	})
	return found
}

func cells(row *html.Node) []*html.Node {
	var found []*html.Node
	for n := row.FirstChild; n != nil; n = n.NextSibling {
		if n.Type == html.ElementNode && (n.Data == "td" || n.Data == "th") {
			found = append(found, n)
		}
	}
	return found
}

func preHeader(pre *html.Node) string {
	var value strings.Builder
	for n := pre.FirstChild; n != nil; n = n.NextSibling {
		if n.Type == html.ElementNode && n.Data == "hr" {
			break
		}
		value.WriteString(text(n))
		if strings.Contains(value.String(), "\n") {
			break
		}
	}
	return value.String()
}

func hasSiblingHR(pre *html.Node, before bool) bool {
	n := pre.NextSibling
	if before {
		n = pre.PrevSibling
	}
	for n != nil {
		if n.Type == html.ElementNode {
			return n.Data == "hr"
		}
		if n.Type == html.TextNode && strings.TrimSpace(n.Data) != "" {
			return false
		}
		if before {
			n = n.PrevSibling
		} else {
			n = n.NextSibling
		}
	}
	return false
}
