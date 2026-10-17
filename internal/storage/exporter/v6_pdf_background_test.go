package exporter

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// testPDF builds a valid pdf with n pages, each carrying its own text so
// pages can be told apart in the extracted content streams.
func testPDF(t *testing.T, pages int, label string) []byte {
	t.Helper()

	var sb strings.Builder
	sb.WriteString(`{"paper":"A4","pages":{`)
	for i := 1; i <= pages; i++ {
		if i > 1 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `"%d":{"content":{"text":[{"value":"%s-%d","position":[100,100],`+
			`"font":{"name":"Helvetica","size":12}}]}}`, i, label, i)
	}
	sb.WriteString(`}}`)

	conf := model.NewDefaultConfiguration()
	var out bytes.Buffer
	if err := api.Create(nil, strings.NewReader(sb.String()), &out, conf); err != nil {
		t.Fatalf("build %s pdf: %v", label, err)
	}
	return out.Bytes()
}

func pageCount(t *testing.T, pdf []byte) int {
	t.Helper()
	n, err := api.PageCount(bytes.NewReader(pdf), model.NewDefaultConfiguration())
	if err != nil {
		t.Fatalf("page count: %v", err)
	}
	return n
}

// pageContent returns the raw content stream of the given page.
func pageContent(t *testing.T, pdf []byte, page int) string {
	t.Helper()
	dir := t.TempDir()
	conf := model.NewDefaultConfiguration()
	if err := api.ExtractContent(bytes.NewReader(pdf), dir, "out.pdf", []string{fmt.Sprint(page)}, conf); err != nil {
		t.Fatalf("extract page %d: %v", page, err)
	}
	b, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("out_Content_page_%d.txt", page)))
	if err != nil {
		t.Fatalf("read page %d content: %v", page, err)
	}
	return string(b)
}

// The ink must land on the page the annotation belongs to and nowhere else.
func TestMergeV6AnnotationsWithPDFStampsInkOnItsPage(t *testing.T) {
	background := testPDF(t, 2, "bg")
	ink := testPDF(t, 1, "ink")

	var out bytes.Buffer
	if err := MergeV6AnnotationsWithPDF(background, ink, map[int]int{1: 2}, &out); err != nil {
		t.Fatalf("merge: %v", err)
	}
	merged := out.Bytes()
	if got := pageCount(t, merged); got != 2 {
		t.Fatalf("page count = %d, want 2", got)
	}

	if c := pageContent(t, merged, 1); strings.Contains(c, "/Subtype /Watermark") {
		t.Error("page 1 got stamped, the ink belongs to page 2")
	}
	if c := pageContent(t, merged, 2); !strings.Contains(c, "/Subtype /Watermark") {
		t.Error("page 2 was not stamped")
	}
}

// Ink pages past the end of the payload are extra notebook pages.
func TestMergeV6AnnotationsWithPDFAppendsExtraPages(t *testing.T) {
	background := testPDF(t, 1, "bg")
	ink := testPDF(t, 2, "ink")

	var out bytes.Buffer
	if err := MergeV6AnnotationsWithPDF(background, ink, map[int]int{1: 1, 2: 2}, &out); err != nil {
		t.Fatalf("merge: %v", err)
	}
	merged := out.Bytes()
	if got := pageCount(t, merged); got != 2 {
		t.Fatalf("page count = %d, want 2", got)
	}
	if c := pageContent(t, merged, 1); !strings.Contains(c, "/Subtype /Watermark") {
		t.Error("page 1 was not stamped")
	}
	if c := pageContent(t, merged, 2); !strings.Contains(c, "ink-2") {
		t.Error("the extra ink page was not appended")
	}
}

// pdfcpu's watermark default paints along the page diagonal. Stamping an
// ink page must not rotate it: the handwriting would come out skewed.
func TestInkWatermarkKeepsOrientation(t *testing.T) {
	ink := testPDF(t, 1, "ink")
	conf := model.NewDefaultConfiguration()
	inkDims, err := api.PageDims(bytes.NewReader(ink), conf)
	if err != nil {
		t.Fatalf("ink dims: %v", err)
	}

	const bgW, bgH = 612.0, 792.0
	scale := math.Min(bgW/inkDims[0].Width, bgH/inkDims[0].Height)

	wm, err := inkWatermark(bytes.NewReader(ink), 1, scale)
	if err != nil {
		t.Fatalf("build watermark: %v", err)
	}
	if wm.Diagonal != model.NoDiagonal {
		t.Fatalf("watermark diagonal = %d, want NoDiagonal: pdfcpu would rotate the ink by the page diagonal angle", wm.Diagonal)
	}
	if wm.Rotation != 0 {
		t.Fatalf("watermark rotation = %v, want 0", wm.Rotation)
	}

	// the transform pdfcpu applies to the ink must be a pure translation,
	// a non zero off diagonal term means the ink got rotated
	wm.Vp = types.RectForDim(bgW, bgH)
	wm.Bb = types.RectForDim(inkDims[0].Width*scale, inkDims[0].Height*scale)
	m := wm.CalcTransformMatrix()
	if m[0][1] != 0 || m[1][0] != 0 {
		t.Fatalf("ink transform matrix rotates the ink:\n%v", m)
	}
}
