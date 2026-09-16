package report

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"
)

// PDF writes the table as a printable document.
//
// PDF is a text format with a byte-offset table at the end, which is the only
// fiddly part: every object's position has to be recorded as it is written.
// Helvetica is one of the fourteen fonts every reader is required to have, so
// nothing is embedded and the output stays a few kilobytes.
//
// Text is measured with the font's real character widths rather than assumed
// to be monospaced, because a right-aligned column of numbers that is not
// actually right-aligned looks like a bug in the report.
const (
	pageWidth  = 595.28 // A4 portrait, in points
	pageHeight = 841.89
	margin     = 40.0

	titleSize  = 16.0
	bodySize   = 9.0
	headerSize = 9.0
	lineHeight = 14.0
)

func PDF(w io.Writer, t Table) error {
	pages := layout(t)

	var (
		out     bytes.Buffer
		offsets []int
	)
	object := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	out.WriteString("%PDF-1.4\n")
	// A comment of high bytes marks the file as binary, so tools that
	// transfer it do not mangle line endings.
	out.WriteString("%\xE2\xE3\xCF\xD3\n")

	// 1 catalog, 2 pages, 3 Helvetica, 4 Helvetica-Bold, then one content
	// stream and one page object per page.
	contentFirst := 5
	pageFirst := contentFirst + len(pages)

	kids := make([]string, 0, len(pages))
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", pageFirst+i))
	}

	object("<< /Type /Catalog /Pages 2 0 R >>")
	object(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>",
		strings.Join(kids, " "), len(pages)))
	object("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>")
	object("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>")

	for _, content := range pages {
		object(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	}
	for i := range pages {
		object(fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] "+
				"/Resources << /Font << /F1 3 0 R /F2 4 0 R >> >> /Contents %d 0 R >>",
			pageWidth, pageHeight, contentFirst+i))
	}

	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xref)

	_, err := w.Write(out.Bytes())
	return err
}

// layout turns the table into one content stream per page.
func layout(t Table) []string {
	widths := columnWidths(t)

	var (
		pages []string
		b     strings.Builder
		y     float64
	)
	pageNo := 0

	startPage := func() {
		pageNo++
		b.Reset()
		y = pageHeight - margin

		if pageNo == 1 {
			b.WriteString(text(margin, y-titleSize, titleSize, true, t.Title))
			y -= titleSize + 8
			if t.Subtitle != "" {
				b.WriteString(text(margin, y-bodySize, bodySize, false, t.Subtitle))
				y -= bodySize + 4
			}
			if !t.Generated.IsZero() {
				b.WriteString(text(margin, y-bodySize, bodySize, false,
					"Generated "+t.Generated.Format(time.RFC1123)))
				y -= bodySize + 4
			}
			y -= 10
			y = drawChart(&b, y, t)
		} else {
			b.WriteString(text(margin, y-bodySize, bodySize, false, t.Title+" (continued)"))
			y -= bodySize + 14
		}

		// Header row, with a rule under it.
		drawRow(&b, y, widths, headerCells(t), t.Columns, true)
		y -= lineHeight
		b.WriteString(line(margin, y+4, pageWidth-margin, y+4))
		y -= 4
	}

	endPage := func() {
		b.WriteString(text(pageWidth-margin-40, margin-10, 8, false,
			fmt.Sprintf("Page %d", pageNo)))
		pages = append(pages, b.String())
	}

	startPage()
	for _, row := range t.Rows {
		if y < margin+60 {
			endPage()
			startPage()
		}
		drawRow(&b, y, widths, row, t.Columns, false)
		y -= lineHeight
	}

	if len(t.Total) > 0 {
		if y < margin+60 {
			endPage()
			startPage()
		}
		b.WriteString(line(margin, y+lineHeight-4, pageWidth-margin, y+lineHeight-4))
		drawRow(&b, y, widths, t.Total, t.Columns, true)
		y -= lineHeight
	}

	for _, note := range t.Notes {
		y -= 8
		for _, wrapped := range wrap(note, pageWidth-2*margin, 8) {
			if y < margin+20 {
				endPage()
				startPage()
			}
			b.WriteString(text(margin, y, 8, false, wrapped))
			y -= 11
		}
	}

	endPage()
	return pages
}

// drawChart draws a horizontal bar chart and returns the y it finished at.
//
// Horizontal rather than vertical because the labels are host names: rotated
// text under a vertical bar is a readability problem this does not need to
// have, and the question the chart answers — which host is the heavy one — is
// a comparison of lengths either way.
func drawChart(b *strings.Builder, y float64, t Table) float64 {
	if len(t.Chart) == 0 {
		return y
	}

	var max float64
	for _, bar := range t.Chart {
		if bar.Value > max {
			max = bar.Value
		}
	}
	if max <= 0 {
		// Every bar would be zero length, which is a blank rectangle
		// pretending to be information.
		return y
	}

	if t.ChartTitle != "" {
		b.WriteString(text(margin, y-headerSize, headerSize, true, t.ChartTitle))
		y -= headerSize + 10
	}

	// The label column is as wide as the longest label, capped so one long
	// host name cannot squeeze every bar down to nothing.
	labelWidth := 0.0
	for _, bar := range t.Chart {
		if w := stringWidth(bar.Label, bodySize); w > labelWidth {
			labelWidth = w
		}
	}
	if maxLabel := (pageWidth - 2*margin) * 0.3; labelWidth > maxLabel {
		labelWidth = maxLabel
	}

	const (
		barHeight = 11.0
		barGap    = 6.0
		valueGap  = 6.0
	)
	valueWidth := 0.0
	for _, bar := range t.Chart {
		if w := stringWidth(bar.Text, bodySize); w > valueWidth {
			valueWidth = w
		}
	}

	trackLeft := margin + labelWidth + valueGap
	trackWidth := pageWidth - margin - valueWidth - valueGap - trackLeft

	for _, bar := range t.Chart {
		rowY := y - barHeight
		b.WriteString(text(margin, rowY+2.5, bodySize, false,
			fit(bar.Label, labelWidth, bodySize)))

		// The track behind every bar makes a small share legible as a
		// small share rather than as a missing bar.
		b.WriteString(rect(trackLeft, rowY, trackWidth, barHeight, 0.90))

		width := trackWidth * bar.Value / max
		if width > 0 && width < 1.5 {
			// A visible sliver, so "almost nothing" and "nothing" do not
			// look the same.
			width = 1.5
		}
		if width > 0 {
			b.WriteString(rect(trackLeft, rowY, width, barHeight, 0.25))
		}

		b.WriteString(text(trackLeft+trackWidth+valueGap, rowY+2.5, bodySize, false, bar.Text))
		y -= barHeight + barGap
	}

	return y - 12
}

// rect fills a rectangle in a grey level, 0 black to 1 white.
func rect(x, y, w, h, grey float64) string {
	return fmt.Sprintf("%.3f %.3f %.3f rg %.2f %.2f %.2f %.2f re f\n", grey, grey, grey, x, y, w, h)
}

func headerCells(t Table) []Cell {
	out := make([]Cell, len(t.Columns))
	for i, c := range t.Columns {
		out[i] = Text(c.Title)
	}
	return out
}

// columnWidths divides the printable width in proportion to the widest cell in
// each column, so a column of host names gets the room a column of percentages
// does not need.
func columnWidths(t Table) []float64 {
	n := len(t.Columns)
	if n == 0 {
		return nil
	}
	needed := make([]float64, n)
	measure := func(cells []Cell) {
		for i, c := range cells {
			if i < n {
				if w := stringWidth(c.Text, bodySize); w > needed[i] {
					needed[i] = w
				}
			}
		}
	}
	measure(headerCells(t))
	for _, r := range t.Rows {
		measure(r)
	}
	measure(t.Total)

	const gutter = 10.0
	total := gutter * float64(n-1)
	for _, w := range needed {
		total += w
	}

	available := pageWidth - 2*margin
	widths := make([]float64, n)
	for i, w := range needed {
		widths[i] = w + gutter
		if total > available {
			// Everything shrinks together rather than one column being
			// truncated, which at least keeps the table readable.
			widths[i] = (w + gutter) * available / total
		}
	}
	return widths
}

func drawRow(b *strings.Builder, y float64, widths []float64, cells []Cell, cols []Column, bold bool) {
	x := margin
	for i, cell := range cells {
		w := 0.0
		if i < len(widths) {
			w = widths[i]
		}
		align := Left
		if i < len(cols) {
			align = cols[i].Align
		}

		s := fit(cell.Text, w-6, bodySize)
		tx := x
		if align == Right {
			tx = x + w - 6 - stringWidth(s, bodySize)
		}
		b.WriteString(text(tx, y, bodySize, bold, s))
		x += w
	}
}

func text(x, y, size float64, bold bool, s string) string {
	font := "F1"
	if bold {
		font = "F2"
	}
	return fmt.Sprintf("BT /%s %.2f Tf %.2f %.2f Td (%s) Tj ET\n",
		font, size, x, y, escapePDF(s))
}

func line(x1, y1, x2, y2 float64) string {
	return fmt.Sprintf("0.8 w 0.75 0.75 0.75 RG %.2f %.2f m %.2f %.2f l S\n", x1, y1, x2, y2)
}

// escapePDF makes a string safe inside a PDF literal and drops anything
// WinAnsiEncoding cannot represent. A host name is operator input, and a
// stray byte would otherwise produce a file no reader will open.
func escapePDF(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20:
			b.WriteByte(' ')
		case r < 0x7f:
			b.WriteRune(r)
		case r <= 0xff:
			// WinAnsi matches Latin-1 in this range.
			fmt.Fprintf(&b, "\\%03o", r)
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

// fit truncates a string to a width, with an ellipsis when it had to cut.
func fit(s string, width, size float64) string {
	if width <= 0 || stringWidth(s, size) <= width {
		return s
	}
	runes := []rune(s)
	for len(runes) > 1 {
		runes = runes[:len(runes)-1]
		if stringWidth(string(runes)+"…", size) <= width {
			return string(runes) + "..."
		}
	}
	return ""
}

// wrap breaks a note on word boundaries.
func wrap(s string, width, size float64) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var (
		lines []string
		cur   string
	)
	for _, word := range words {
		candidate := word
		if cur != "" {
			candidate = cur + " " + word
		}
		if stringWidth(candidate, size) > width && cur != "" {
			lines = append(lines, cur)
			cur = word
			continue
		}
		cur = candidate
	}
	return append(lines, cur)
}

// helveticaWidths is the advance width of each WinAnsi character in Helvetica,
// in thousandths of the font size, for the printable ASCII range. These are
// the values from the font's own metrics; without them every alignment in the
// document would be a guess.
var helveticaWidths = [95]int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584,
}

func stringWidth(s string, size float64) float64 {
	total := 0
	for _, r := range s {
		switch {
		case r >= 0x20 && r < 0x7f:
			total += helveticaWidths[r-0x20]
		case r > 0x7f:
			// Everything outside ASCII is rendered as one fallback glyph
			// or a Latin-1 character; 556 is Helvetica's common width and
			// close enough for a column that is padded anyway.
			total += 556
		}
	}
	return float64(total) * size / 1000
}
