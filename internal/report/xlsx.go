package report

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// XLSX writes the table as a spreadsheet.
//
// An .xlsx is a zip of XML parts, and only a handful of them are required for
// a file every spreadsheet program will open. Strings are written inline
// rather than through a shared-string table: it costs a few bytes on a report
// with repeated values and removes a whole part and its indirection.
func XLSX(w io.Writer, t Table) error {
	z := zip.NewWriter(w)

	parts := []struct {
		name string
		body string
	}{
		{"[Content_Types].xml", contentTypes},
		{"_rels/.rels", rootRels},
		{"xl/workbook.xml", workbook(t.Title)},
		{"xl/_rels/workbook.xml.rels", workbookRels},
		{"xl/styles.xml", styles},
		{"xl/worksheets/sheet1.xml", sheet(t)},
	}
	for _, p := range parts {
		f, err := z.Create(p.name)
		if err != nil {
			return fmt.Errorf("create %s: %w", p.name, err)
		}
		if _, err := io.WriteString(f, p.body); err != nil {
			return fmt.Errorf("write %s: %w", p.name, err)
		}
	}
	return z.Close()
}

const contentTypes = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
<Default Extension="xml" ContentType="application/xml"/>
<Override PartName="/xl/workbook.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"/>
<Override PartName="/xl/worksheets/sheet1.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.worksheet+xml"/>
<Override PartName="/xl/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.spreadsheetml.styles+xml"/>
</Types>`

const rootRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="xl/workbook.xml"/>
</Relationships>`

const workbookRels = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/worksheet" Target="worksheets/sheet1.xml"/>
<Relationship Id="rId2" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/>
</Relationships>`

// styles defines four cell formats, referenced by index from the sheet:
// 0 plain, 1 bold, 2 plain with thousands separators, 3 bold with them.
const styles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">
<numFmts count="1"><numFmt numFmtId="164" formatCode="#,##0"/></numFmts>
<fonts count="2"><font><sz val="11"/><name val="Calibri"/></font><font><b/><sz val="11"/><name val="Calibri"/></font></fonts>
<fills count="2"><fill><patternFill patternType="none"/></fill><fill><patternFill patternType="gray125"/></fill></fills>
<borders count="1"><border><left/><right/><top/><bottom/><diagonal/></border></borders>
<cellStyleXfs count="1"><xf numFmtId="0" fontId="0" fillId="0" borderId="0"/></cellStyleXfs>
<cellXfs count="4">
<xf numFmtId="0" fontId="0" fillId="0" borderId="0" xfId="0"/>
<xf numFmtId="0" fontId="1" fillId="0" borderId="0" xfId="0" applyFont="1"/>
<xf numFmtId="164" fontId="0" fillId="0" borderId="0" xfId="0" applyNumberFormat="1"/>
<xf numFmtId="164" fontId="1" fillId="0" borderId="0" xfId="0" applyNumberFormat="1" applyFont="1"/>
</cellXfs>
</styleSheet>`

func workbook(title string) string {
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships">
<sheets><sheet name="` + escapeXML(sheetName(title)) + `" sheetId="1" r:id="rId1"/></sheets>
</workbook>`
}

// sheetName trims a title to what Excel accepts as a tab name: 31 characters,
// and none of []:*?/\ — a file that will not open is a worse report than one
// with a shortened tab.
func sheetName(title string) string {
	name := strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '-'
		}
		return r
	}, title)
	if name == "" {
		name = "Report"
	}
	runes := []rune(name)
	if len(runes) > 31 {
		runes = runes[:31]
	}
	return string(runes)
}

func sheet(t Table) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`)

	// Column widths, so the first thing an operator sees is not ######.
	b.WriteString(`<cols>`)
	for i, c := range t.Columns {
		width := c.Width
		if width <= 0 {
			width = len([]rune(c.Title)) + 2
		}
		fmt.Fprintf(&b, `<col min="%d" max="%d" width="%d" customWidth="1"/>`, i+1, i+1, width)
	}
	b.WriteString(`</cols><sheetData>`)

	row := 1
	// The title and window go in the sheet as well as in the filename: a
	// spreadsheet gets pasted into a mail with its context lost otherwise.
	writeRow(&b, row, []Cell{Text(t.Title)}, []Column{{}}, true)
	row++
	if t.Subtitle != "" {
		writeRow(&b, row, []Cell{Text(t.Subtitle)}, []Column{{}}, false)
		row++
	}
	if !t.Generated.IsZero() {
		writeRow(&b, row, []Cell{Text("Generated " + t.Generated.Format(time.RFC1123))},
			[]Column{{}}, false)
		row++
	}
	row++ // one blank line before the grid

	header := make([]Cell, len(t.Columns))
	for i, c := range t.Columns {
		header[i] = Text(c.Title)
	}
	writeRow(&b, row, header, t.Columns, true)
	row++

	for _, r := range t.Rows {
		writeRow(&b, row, r, t.Columns, false)
		row++
	}
	if len(t.Total) > 0 {
		writeRow(&b, row, t.Total, t.Columns, true)
		row++
	}

	for _, note := range t.Notes {
		row++
		writeRow(&b, row, []Cell{Text(note)}, []Column{{}}, false)
	}

	b.WriteString(`</sheetData></worksheet>`)
	return b.String()
}

func writeRow(b *strings.Builder, row int, cells []Cell, cols []Column, bold bool) {
	fmt.Fprintf(b, `<row r="%d">`, row)
	for i, cell := range cells {
		ref := cellRef(i, row)
		numeric := i < len(cols) && cols[i].Number && cell.Text != ""

		switch {
		case numeric:
			style := 2
			if bold {
				style = 3
			}
			// Stored as a number so the column can be summed. The text
			// form ("47 MB") is deliberately dropped here: a spreadsheet
			// that shows a tidy string it cannot add up is worse than one
			// that shows the figure.
			fmt.Fprintf(b, `<c r="%s" s="%d"><v>%s</v></c>`,
				ref, style, strconv.FormatFloat(cell.Value, 'f', -1, 64))
		default:
			style := 0
			if bold {
				style = 1
			}
			fmt.Fprintf(b, `<c r="%s" s="%d" t="inlineStr"><is><t xml:space="preserve">%s</t></is></c>`,
				ref, style, escapeXML(cell.Text))
		}
	}
	b.WriteString(`</row>`)
}

// cellRef turns a zero-based column index and a one-based row into A1, Z9,
// AA1 and so on.
func cellRef(col, row int) string {
	name := ""
	for n := col; ; n = n/26 - 1 {
		name = string(rune('A'+n%26)) + name
		if n < 26 {
			break
		}
	}
	return name + strconv.Itoa(row)
}

func escapeXML(s string) string {
	var b strings.Builder
	// Control characters are not legal in XML 1.0 at all, and a host name
	// is operator input, so they are dropped rather than escaped.
	clean := strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
			return -1
		}
		return r
	}, s)
	xml.EscapeText(&b, []byte(clean))
	return b.String()
}
