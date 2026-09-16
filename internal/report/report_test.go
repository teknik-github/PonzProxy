package report

import (
	"archive/zip"
	"bytes"
	"io"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sample() Table {
	return Table{
		Title:      "Traffic used",
		Subtitle:   "1 September 2026 to 8 September 2026 (7 days)",
		ChartTitle: "Total traffic by host",
		Generated:  time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC),
		Columns: []Column{
			{Title: "Host", Width: 28},
			{Title: "Requests", Align: Right, Number: true},
			{Title: "Total (bytes)", Align: Right, Number: true},
			{Title: "Share", Align: Right},
		},
		Rows: [][]Cell{
			{Text("storefront"), Number("476802", 476802), Number("46716739", 46716739), Text("65.0%")},
			{Text("api"), Number("232052", 232052), Number("22085414", 22085414), Text("30.6%")},
			{Text("(unmatched)"), Number("6797", 6797), Number("497504", 497504), Text("0.7%")},
		},
		Total: []Cell{
			Text("All hosts"), Number("715651", 715651), Number("69299657", 69299657), Text("100.0%"),
		},
		Chart: []Bar{
			{Label: "storefront", Value: 46716739, Text: "47 MB"},
			{Label: "api", Value: 22085414, Text: "22 MB"},
			{Label: "(unmatched)", Value: 497504, Text: "498 kB"},
		},
		Notes: []string{"Counted at the proxy, headers included."},
	}
}

/* ------------------------------------------------------------------ xlsx -- */

// readPart returns one file from the written archive.
func readPart(t *testing.T, data []byte, name string) string {
	t.Helper()
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("the output is not a valid zip: %v", err)
	}
	for _, f := range z.File {
		if f.Name != name {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", name, err)
		}
		defer rc.Close()
		body, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		return string(body)
	}
	t.Fatalf("%s is missing from the archive", name)
	return ""
}

func TestXLSXHasEveryPartAReaderNeeds(t *testing.T) {
	var buf bytes.Buffer
	if err := XLSX(&buf, sample()); err != nil {
		t.Fatalf("XLSX: %v", err)
	}

	// Any of these missing produces a file Excel refuses to open at all,
	// which is the failure mode worth guarding: it is invisible until
	// someone tries.
	for _, part := range []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"xl/workbook.xml",
		"xl/_rels/workbook.xml.rels",
		"xl/styles.xml",
		"xl/worksheets/sheet1.xml",
	} {
		readPart(t, buf.Bytes(), part)
	}
}

func TestXLSXStoresNumbersAsNumbers(t *testing.T) {
	var buf bytes.Buffer
	if err := XLSX(&buf, sample()); err != nil {
		t.Fatalf("XLSX: %v", err)
	}
	sheet := readPart(t, buf.Bytes(), "xl/worksheets/sheet1.xml")

	// A spreadsheet whose figures are text looks right and cannot be summed,
	// which defeats the point of offering a spreadsheet at all.
	if !strings.Contains(sheet, "<v>46716739</v>") {
		t.Error("the byte total was not stored as a number")
	}
	if strings.Contains(sheet, `<t xml:space="preserve">46716739</t>`) {
		t.Error("the byte total was stored as text")
	}
	// The host name is text and must stay text.
	if !strings.Contains(sheet, "storefront") {
		t.Error("the host name is missing")
	}
}

func TestXLSXEscapesOperatorInput(t *testing.T) {
	tbl := sample()
	// A host name is whatever the operator typed. Unescaped, this breaks
	// the XML and the file will not open.
	tbl.Rows[0][0] = Text(`a & b <script> "quoted" 'single'`)

	var buf bytes.Buffer
	if err := XLSX(&buf, tbl); err != nil {
		t.Fatalf("XLSX: %v", err)
	}
	sheet := readPart(t, buf.Bytes(), "xl/worksheets/sheet1.xml")
	if strings.Contains(sheet, "<script>") {
		t.Error("a host name was written into the XML unescaped")
	}
	if !strings.Contains(sheet, "&amp;") {
		t.Error("the ampersand was not escaped")
	}
}

func TestSheetNameIsAcceptable(t *testing.T) {
	cases := map[string]string{
		"Traffic used":          "Traffic used",
		"a/b:c*d?e[f]g":         "a-b-c-d-e-f-g",
		"":                      "Report",
		strings.Repeat("x", 40): strings.Repeat("x", 31),
	}
	for in, want := range cases {
		if got := sheetName(in); got != want {
			t.Errorf("sheetName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCellRefCountsPastZ(t *testing.T) {
	cases := map[int]string{0: "A1", 25: "Z1", 26: "AA1", 27: "AB1", 51: "AZ1", 52: "BA1"}
	for col, want := range cases {
		if got := cellRef(col, 1); got != want {
			t.Errorf("cellRef(%d, 1) = %q, want %q", col, got, want)
		}
	}
}

/* ------------------------------------------------------------------- pdf -- */

var xrefEntry = regexp.MustCompile(`(?m)^(\d{10}) 00000 n $`)

func TestPDFOffsetsPointAtTheirObjects(t *testing.T) {
	var buf bytes.Buffer
	if err := PDF(&buf, sample()); err != nil {
		t.Fatalf("PDF: %v", err)
	}
	out := buf.Bytes()

	// The cross-reference table is the one part of a PDF that a reader
	// cannot recover from being wrong, and it is a byte offset computed by
	// hand — so it is worth checking that each one lands on "N 0 obj".
	matches := xrefEntry.FindAllSubmatch(out, -1)
	if len(matches) == 0 {
		t.Fatal("no xref entries were written")
	}
	for i, m := range matches {
		off, err := strconv.Atoi(string(m[1]))
		if err != nil {
			t.Fatalf("entry %d is not a number: %v", i, err)
		}
		if off < 0 || off >= len(out) {
			t.Fatalf("entry %d points outside the file: %d", i, off)
		}
		want := []byte(strconv.Itoa(i+1) + " 0 obj")
		if !bytes.HasPrefix(out[off:], want) {
			t.Errorf("entry %d points at %q, want %q",
				i, out[off:min(off+20, len(out))], want)
		}
	}
}

func TestPDFIsWellFormedEnough(t *testing.T) {
	var buf bytes.Buffer
	if err := PDF(&buf, sample()); err != nil {
		t.Fatalf("PDF: %v", err)
	}
	out := buf.String()

	for _, want := range []string{"%PDF-1.4", "/Type /Catalog", "/Type /Pages",
		"/BaseFont /Helvetica", "trailer", "startxref", "%%EOF"} {
		if !strings.Contains(out, want) {
			t.Errorf("the document is missing %q", want)
		}
	}
	// The title, a host name and the chart's value label should all appear.
	for _, want := range []string{"Traffic used", "storefront", "47 MB"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q does not appear in the document", want)
		}
	}
	// The chart draws filled rectangles; without them there is no chart.
	if !strings.Contains(out, " re f") {
		t.Error("no rectangles were drawn, so the chart is missing")
	}
}

func TestPDFEscapesParenthesesAndBackslashes(t *testing.T) {
	tbl := sample()
	// Unescaped, a bracket ends the string literal early and every reader
	// rejects the file.
	tbl.Rows[0][0] = Text(`shop (eu) \ test`)

	var buf bytes.Buffer
	if err := PDF(&buf, tbl); err != nil {
		t.Fatalf("PDF: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `shop \(eu\) \\ test`) {
		t.Error("brackets or the backslash were not escaped")
	}
}

func TestPDFPaginatesLongReports(t *testing.T) {
	tbl := sample()
	tbl.Rows = nil
	for i := range 200 {
		tbl.Rows = append(tbl.Rows, []Cell{
			Text("host-" + strconv.Itoa(i)),
			Number("1", 1), Number("2", 2), Text("0.5%"),
		})
	}

	var buf bytes.Buffer
	if err := PDF(&buf, tbl); err != nil {
		t.Fatalf("PDF: %v", err)
	}
	out := buf.String()
	if got := strings.Count(out, "/Type /Page "); got < 2 {
		t.Errorf("200 rows produced %d pages; they cannot all fit on one", got)
	}
	// Every page after the first must say what it is a continuation of.
	if !strings.Contains(out, "continued") {
		t.Error("later pages do not repeat the title")
	}
}

func TestChartWithNoTrafficDrawsNothing(t *testing.T) {
	tbl := sample()
	for i := range tbl.Chart {
		tbl.Chart[i].Value = 0
	}

	var buf bytes.Buffer
	if err := PDF(&buf, tbl); err != nil {
		t.Fatalf("PDF: %v", err)
	}
	// A row of empty tracks is a chart that says nothing while looking like
	// it says something.
	if strings.Contains(buf.String(), "Total traffic by host") {
		t.Error("a chart of zeroes was drawn anyway")
	}
}

func TestStringWidthIsNotMonospaced(t *testing.T) {
	// If these came out equal, every right-aligned column would be wrong by
	// a few points and the report would look subtly broken.
	if stringWidth("iii", 10) >= stringWidth("WWW", 10) {
		t.Error("narrow and wide characters measured the same")
	}
	if stringWidth("", 10) != 0 {
		t.Error("the empty string has width")
	}
}

func TestFitTruncatesRatherThanOverflowing(t *testing.T) {
	long := strings.Repeat("shop.example.com ", 10)
	got := fit(long, 60, bodySize)
	if stringWidth(got, bodySize) > 60 {
		t.Errorf("fit returned %q, which is still too wide", got)
	}
	if !strings.HasSuffix(got, "...") {
		t.Errorf("a truncated string should say so, got %q", got)
	}
	// Something that already fits must come back untouched.
	if got := fit("api", 200, bodySize); got != "api" {
		t.Errorf("fit shortened a string that fits: %q", got)
	}
}

func TestEmptyTableStillProducesValidFiles(t *testing.T) {
	empty := Table{Title: "Traffic used", Generated: time.Now()}

	var pdf bytes.Buffer
	if err := PDF(&pdf, empty); err != nil {
		t.Fatalf("PDF of an empty report: %v", err)
	}
	if !strings.Contains(pdf.String(), "%%EOF") {
		t.Error("the empty PDF is not terminated")
	}

	var xlsx bytes.Buffer
	if err := XLSX(&xlsx, empty); err != nil {
		t.Fatalf("XLSX of an empty report: %v", err)
	}
	readPart(t, xlsx.Bytes(), "xl/worksheets/sheet1.xml")
}
