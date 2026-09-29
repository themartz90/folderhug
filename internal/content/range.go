package content

import (
	"strconv"
	"strings"
)

type byteRange struct{ start, end, suffix int64 }
type contentRange struct{ start, end, total int64 }

func decimal(text string) (int64, bool) {
	if text == "" {
		return 0, false
	}
	for _, ch := range text {
		if ch < '0' || ch > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(text, 10, 64)
	return n, err == nil
}

func parseRange(text string) (*byteRange, error) {
	if text == "" {
		return nil, nil
	}
	if !strings.HasPrefix(text, "bytes=") || strings.Contains(text, ",") {
		return nil, failure(400, "invalid_request", "Only one byte range is supported.")
	}
	a, b, ok := strings.Cut(strings.TrimPrefix(text, "bytes="), "-")
	if !ok {
		return nil, failure(400, "invalid_request", "The byte range is invalid.")
	}
	if a == "" {
		n, ok := decimal(b)
		if !ok || n == 0 {
			return nil, failure(400, "invalid_request", "The byte range is invalid.")
		}
		return &byteRange{suffix: n}, nil
	}
	start, ok := decimal(a)
	if !ok {
		return nil, failure(400, "invalid_request", "The byte range is invalid.")
	}
	end := int64(-1)
	if b != "" {
		var valid bool
		end, valid = decimal(b)
		if !valid || end < start {
			return nil, failure(400, "invalid_request", "The byte range is invalid.")
		}
	}
	return &byteRange{start: start, end: end}, nil
}

func parseContentRange(text string) (contentRange, bool) {
	if !strings.HasPrefix(text, "bytes ") {
		return contentRange{}, false
	}
	interval, totalText, ok := strings.Cut(strings.TrimPrefix(text, "bytes "), "/")
	if !ok {
		return contentRange{}, false
	}
	a, b, ok := strings.Cut(interval, "-")
	if !ok {
		return contentRange{}, false
	}
	start, x := decimal(a)
	end, y := decimal(b)
	total, z := decimal(totalText)
	return contentRange{start, end, total}, x && y && z && start <= end && end < total
}

func (r *byteRange) matches(c contentRange) bool {
	if r == nil {
		return false
	}
	if r.suffix > 0 {
		return c.start == max(0, c.total-r.suffix) && c.end == c.total-1
	}
	end := r.end
	if end < 0 || end >= c.total {
		end = c.total - 1
	}
	return c.start == r.start && c.end == end
}
