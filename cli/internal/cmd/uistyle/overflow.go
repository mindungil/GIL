package uistyle

import (
	"fmt"
	"io"
)

// OverflowHint prints a "+ N more" dimmed hint line when total exceeds
// the number of items shown. It is used by both the no-arg summary
// (summary.go) and the status renderer (status_render.go) so the
// formatting lives in one place.
//
// When total <= shown nothing is written.
func OverflowHint(w io.Writer, p Palette, total, shown int) {
	if total <= shown {
		return
	}
	extra := total - shown
	fmt.Fprintf(w, "   %s\n", p.Dim(fmt.Sprintf("›  + %d more", extra)))
}
