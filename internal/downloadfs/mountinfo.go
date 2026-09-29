package downloadfs

import (
	"bufio"
	"fmt"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type DiscoveredMount struct {
	Path  string
	Label string
}

type mountRecord struct {
	id, parent int
	device     string
	root       string
	target     string
	options    string
	fsType     string
	super      string
}

func parseMountInfo(reader io.Reader) ([]mountRecord, error) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), 128<<10)
	records := make([]mountRecord, 0, 128)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), " - ", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid mountinfo record")
		}
		left, right := strings.Fields(parts[0]), strings.Fields(parts[1])
		if len(left) < 6 || len(right) < 3 {
			return nil, fmt.Errorf("invalid mountinfo fields")
		}
		id, err := strconv.Atoi(left[0])
		if err != nil || id < 1 {
			return nil, fmt.Errorf("invalid mountinfo ID")
		}
		parent, err := strconv.Atoi(left[1])
		if err != nil || parent < 0 {
			return nil, fmt.Errorf("invalid mountinfo parent")
		}
		root, err := unescapeMountInfo(left[3])
		if err != nil {
			return nil, err
		}
		target, err := unescapeMountInfo(left[4])
		if err != nil {
			return nil, err
		}
		records = append(records, mountRecord{id, parent, left[2], root, target, left[5], right[0], right[2]})
		if len(records) > 8192 {
			return nil, fmt.Errorf("too many mountinfo records")
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func unescapeMountInfo(raw string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			out.WriteByte(raw[i])
			continue
		}
		if i+3 >= len(raw) || raw[i+1] < '0' || raw[i+1] > '7' || raw[i+2] < '0' || raw[i+2] > '7' || raw[i+3] < '0' || raw[i+3] > '7' {
			return "", fmt.Errorf("invalid mountinfo escape")
		}
		value, err := strconv.ParseUint(raw[i+1:i+4], 8, 8)
		if err != nil {
			return "", fmt.Errorf("invalid mountinfo escape")
		}
		out.WriteByte(byte(value))
		i += 3
	}
	if !utf8.ValidString(out.String()) {
		return "", fmt.Errorf("invalid mountinfo text")
	}
	return out.String(), nil
}

func directMountCandidate(record mountRecord) bool {
	if !path.IsAbs(record.target) || path.Clean(record.target) != record.target || path.Dir(record.target) != "/mnt" {
		return false
	}
	label := path.Base(record.target)
	if label == "." || label == ".." || len(label) > 120 || strings.IndexFunc(label, unicode.IsControl) >= 0 {
		return false
	}
	if !optionPresent(record.options, "rw") || !optionPresent(record.super, "rw") {
		return false
	}
	switch record.fsType {
	case "proc", "sysfs", "devpts", "devtmpfs", "cgroup", "cgroup2", "securityfs", "mqueue", "hugetlbfs", "tracefs", "debugfs", "configfs", "bpf", "pstore", "efivarfs", "fusectl", "ramfs", "tmpfs", "overlay", "autofs", "nsfs":
		return false
	}
	return true
}

func optionPresent(options, name string) bool {
	for option := range strings.SplitSeq(options, ",") {
		if option == name {
			return true
		}
	}
	return false
}

func sourceFor(records []mountRecord, target string) (device, source string, ok bool) {
	var best *mountRecord
	for i := range records {
		r := &records[i]
		if target == r.target || strings.HasPrefix(target, strings.TrimSuffix(r.target, "/")+"/") {
			if best == nil || len(r.target) > len(best.target) {
				best = r
			}
		}
	}
	if best == nil {
		return "", "", false
	}
	relative := strings.TrimPrefix(target, best.target)
	return best.device, path.Clean(best.root + "/" + strings.TrimPrefix(relative, "/")), true
}

func sourceOverlap(a, b string) bool {
	return a == b || strings.HasPrefix(a, strings.TrimSuffix(b, "/")+"/") || strings.HasPrefix(b, strings.TrimSuffix(a, "/")+"/")
}

func protectedSourceOverlap(record mountRecord, records []mountRecord, targets []string) bool {
	for _, target := range targets {
		device, source, ok := sourceFor(records, target)
		if ok && device == record.device && sourceOverlap(record.root, source) {
			return true
		}
	}
	return false
}

func candidateRecords(records []mountRecord) []mountRecord {
	selected := make([]mountRecord, 0)
	for _, record := range records {
		if directMountCandidate(record) {
			selected = append(selected, record)
		}
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].target < selected[j].target })
	return selected
}
