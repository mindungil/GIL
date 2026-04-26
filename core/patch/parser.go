package patch

import (
	"strings"
)

const (
	BeginPatchMarker         = "*** Begin Patch"
	EndPatchMarker           = "*** End Patch"
	AddFileMarker            = "*** Add File: "
	DeleteFileMarker         = "*** Delete File: "
	UpdateFileMarker         = "*** Update File: "
	MoveToMarker             = "*** Move to: "
	EOFMarker                = "*** End of File"
	ChangeContextMarker      = "@@ "
	EmptyChangeContextMarker = "@@"
)

// Parse parses a patch in strict mode. The first line must be
// "*** Begin Patch" and the last must be "*** End Patch" (trimmed).
func Parse(input string) (*Patch, error) {
	lines := strings.Split(strings.TrimRight(input, "\n"), "\n")
	if err := checkBoundaries(lines); err != nil {
		return nil, err
	}
	body := lines[1 : len(lines)-1]
	return parseBody(body)
}

func checkBoundaries(lines []string) error {
	if len(lines) < 2 {
		return &ParseError{Kind: ErrInvalidPatch, Message: "patch is too short"}
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if first != BeginPatchMarker {
		return &ParseError{Kind: ErrInvalidPatch, Message: "the first line of the patch must be '" + BeginPatchMarker + "'"}
	}
	if last != EndPatchMarker {
		return &ParseError{Kind: ErrInvalidPatch, Message: "the last line of the patch must be '" + EndPatchMarker + "'"}
	}
	return nil
}

// parseBody walks body lines (after Begin/End trimming) and accumulates hunks.
// The base line number for error reporting is 2 (the line after Begin Patch).
func parseBody(body []string) (*Patch, error) {
	p := &Patch{}
	i := 0
	baseLine := 2
	for i < len(body) {
		// Skip completely blank lines between hunks (mirrors Codex parse_one_hunk
		// blank-line skipping within UpdateFile chunk loops).
		if strings.TrimSpace(body[i]) == "" {
			i++
			continue
		}
		h, n, err := parseOneHunk(body[i:], baseLine+i)
		if err != nil {
			return nil, err
		}
		p.Hunks = append(p.Hunks, h)
		i += n
	}
	if len(p.Hunks) == 0 {
		return nil, &ParseError{Kind: ErrInvalidPatch, Message: "patch contains no hunks"}
	}
	return p, nil
}

// parseOneHunk attempts to parse a single hunk from the start of lines.
// Returns the parsed hunk, the number of lines consumed, and any error.
// Corresponds to Codex parse_one_hunk (strict mode; allow_incomplete omitted).
func parseOneHunk(lines []string, lineNumber int) (Hunk, int, error) {
	first := strings.TrimRight(lines[0], " \t")

	switch {
	case strings.HasPrefix(first, AddFileMarker):
		path := strings.TrimSpace(first[len(AddFileMarker):])
		var contents strings.Builder
		n := 1
		for n < len(lines) {
			if !strings.HasPrefix(lines[n], "+") {
				break
			}
			contents.WriteString(lines[n][1:])
			contents.WriteString("\n")
			n++
		}
		return Hunk{Kind: HunkAddFile, Path: path, AddContents: contents.String()}, n, nil

	case strings.HasPrefix(first, DeleteFileMarker):
		path := strings.TrimSpace(first[len(DeleteFileMarker):])
		return Hunk{Kind: HunkDeleteFile, Path: path}, 1, nil

	case strings.HasPrefix(first, UpdateFileMarker):
		path := strings.TrimSpace(first[len(UpdateFileMarker):])
		n := 1

		// Optional "*** Move to: " line.
		movePath := ""
		if n < len(lines) && strings.HasPrefix(lines[n], MoveToMarker) {
			movePath = strings.TrimSpace(lines[n][len(MoveToMarker):])
			n++
		}

		// Read chunks until the next *** marker (next hunk) or end of body.
		var chunks []UpdateChunk
		for n < len(lines) {
			if strings.TrimSpace(lines[n]) == "" {
				n++
				continue
			}
			if strings.HasPrefix(lines[n], "*") {
				break
			}
			chunk, consumed, err := parseUpdateChunk(lines[n:], lineNumber+n, len(chunks) == 0)
			if err != nil {
				return Hunk{}, 0, err
			}
			chunks = append(chunks, chunk)
			n += consumed
		}

		if len(chunks) == 0 {
			return Hunk{}, 0, &ParseError{
				Kind:       ErrInvalidHunk,
				LineNumber: lineNumber,
				Message:    "update file hunk for path '" + path + "' is empty",
			}
		}
		return Hunk{
			Kind:     HunkUpdateFile,
			Path:     path,
			MovePath: movePath,
			Chunks:   chunks,
		}, n, nil
	}

	return Hunk{}, 0, &ParseError{
		Kind:       ErrInvalidHunk,
		LineNumber: lineNumber,
		Message: "'" + first + "' is not a valid hunk header. Valid headers: '" +
			AddFileMarker + "{path}', '" +
			DeleteFileMarker + "{path}', '" +
			UpdateFileMarker + "{path}'",
	}
}

// parseUpdateChunk parses one @@ chunk within an UpdateFile hunk.
// allowMissingContext permits the first chunk to omit the @@ marker.
// Corresponds to Codex parse_update_file_chunk.
func parseUpdateChunk(lines []string, lineNumber int, allowMissingContext bool) (UpdateChunk, int, error) {
	if len(lines) == 0 {
		return UpdateChunk{}, 0, &ParseError{
			Kind:       ErrInvalidHunk,
			LineNumber: lineNumber,
			Message:    "update hunk does not contain any lines",
		}
	}

	var change UpdateChunk
	start := 0

	switch {
	case lines[0] == EmptyChangeContextMarker:
		// bare "@@" — no context string
		start = 1
	case strings.HasPrefix(lines[0], ChangeContextMarker):
		// "@@ <context>"
		change.ChangeContext = lines[0][len(ChangeContextMarker):]
		start = 1
	default:
		if !allowMissingContext {
			return UpdateChunk{}, 0, &ParseError{
				Kind:       ErrInvalidHunk,
				LineNumber: lineNumber,
				Message:    "expected update hunk to start with a @@ context marker, got: '" + lines[0] + "'",
			}
		}
		// First chunk may omit the @@ — start = 0.
	}

	if start >= len(lines) {
		return UpdateChunk{}, 0, &ParseError{
			Kind:       ErrInvalidHunk,
			LineNumber: lineNumber + 1,
			Message:    "update hunk does not contain any lines",
		}
	}

	parsed := 0
	for _, line := range lines[start:] {
		switch {
		case line == EOFMarker:
			if parsed == 0 {
				return UpdateChunk{}, 0, &ParseError{
					Kind:       ErrInvalidHunk,
					LineNumber: lineNumber + 1,
					Message:    "update hunk does not contain any lines",
				}
			}
			change.IsEndOfFile = true
			parsed++
			// EOF marker terminates this chunk.
			goto done

		case line == "":
			// Empty line → empty context entry (both old and new).
			change.OldLines = append(change.OldLines, "")
			change.NewLines = append(change.NewLines, "")
			parsed++

		default:
			switch line[0] {
			case ' ':
				change.OldLines = append(change.OldLines, line[1:])
				change.NewLines = append(change.NewLines, line[1:])
			case '+':
				change.NewLines = append(change.NewLines, line[1:])
			case '-':
				change.OldLines = append(change.OldLines, line[1:])
			default:
				if parsed == 0 {
					return UpdateChunk{}, 0, &ParseError{
						Kind:       ErrInvalidHunk,
						LineNumber: lineNumber + 1,
						Message: "unexpected line in update hunk: '" + line +
							"'. Every line should start with ' ' (context), '+' (added), or '-' (removed)",
					}
				}
				// Unrecognised prefix → start of the next chunk; hand off.
				goto done
			}
			parsed++
		}
	}

done:
	return change, start + parsed, nil
}
