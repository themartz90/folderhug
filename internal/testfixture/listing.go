package testfixture

import (
	"bytes"
	"fmt"
	"html"
)

type listingEntry struct {
	Name      string
	Href      string
	Size      int64
	Directory bool
}

func renderListing(format Format, directory string, entries []listingEntry) []byte {
	var out bytes.Buffer
	title := "Index of " + html.EscapeString(directory)
	fmt.Fprintf(&out, "<!DOCTYPE html>\n<html><head><title>%s</title></head><body>\n<h1>%s</h1>\n", title, title)
	if format == ApacheTable {
		out.WriteString("<table>\n<tr><th><a href=\"?C=N;O=D\">Name</a></th><th>Last modified</th><th>Size</th></tr>\n<tr><td><a href=\"../\">Parent Directory</a></td><td></td><td>-</td></tr>\n")
	} else if format == ApachePre {
		out.WriteString("<pre><a href=\"?C=N;O=D\">Name</a>                    Last modified      Size\n<hr><a href=\"../\">Parent Directory</a>                             -\n")
	} else {
		out.WriteString("<pre><a href=\"../\">../</a>\n")
	}
	for _, entry := range entries {
		size := fmt.Sprint(entry.Size)
		if entry.Directory {
			size = "-"
		}
		link := fmt.Sprintf("<a href=\"%s\">%s</a>", html.EscapeString(entry.Href), html.EscapeString(entry.Name))
		switch format {
		case ApacheTable:
			fmt.Fprintf(&out, "<tr><td>%s</td><td>2026-01-02 12:30</td><td>%s</td></tr>\n", link, size)
		case ApachePre:
			fmt.Fprintf(&out, "%s  02-Jan-2026 12:30  %s\n", link, size)
		case Nginx:
			fmt.Fprintf(&out, "%s  02-Jan-2026 12:30  %s\n", link, size)
		}
	}
	if format == ApacheTable {
		out.WriteString("</table>\n")
	} else if format == ApachePre {
		out.WriteString("<hr></pre>\n")
	} else {
		out.WriteString("</pre>\n")
	}
	out.WriteString("</body></html>\n")
	return out.Bytes()
}
