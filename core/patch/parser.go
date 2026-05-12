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
		return &ParseError{
			Kind: ErrInvalidPatch,
			Message: "patch is too short. apply_patch needs at minimum:\n" +
				"  *** Begin Patch\n" +
				"  *** Update File: <path>  (or '*** Add File: <path>' / '*** Delete File: <path>')\n" +
				"  ...\n" +
				"  *** End Patch",
		}
	}
	first := strings.TrimSpace(lines[0])
	last := strings.TrimSpace(lines[len(lines)-1])
	if first != BeginPatchMarker {
		return &ParseError{
			Kind: ErrInvalidPatch,
			Message: "apply_patch needs the exact header '" + BeginPatchMarker +
				"' on line 1 (got: '" + truncateForMsg(first) + "'). " +
				"Each section is then '*** Update File: <path>' / '*** Add File: <path>' / '*** Delete File: <path>', and the patch ends with '" + EndPatchMarker + "'.",
		}
	}
	if last != EndPatchMarker {
		return &ParseError{
			Kind: ErrInvalidPatch,
			Message: "apply_patch must end with '" + EndPatchMarker +
				"' on its own line (got: '" + truncateForMsg(last) + "'). " +
				"Add '" + EndPatchMarker + "' as the final line.",
		}
	}
	return nil
}

// truncateForMsg keeps error messages readable when the offending line is a
// long blob. Returns up to 80 chars; longer strings get an ellipsis.
func truncateForMsg(s string) string {
	const max = 80
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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
		return nil, &ParseError{
			Kind: ErrInvalidPatch,
			Message: "patch contains no hunks. Add at least one section between '*** Begin Patch' and '*** End Patch':\n" +
				"  *** Update File: <path>\n" +
				"  @@ <optional description>\n" +
				"   <context line>\n" +
				"  -<line to remove>\n" +
				"  +<line to add>",
		}
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
				Message: "update file hunk for path '" + path + "' is empty. " +
					"After '" + UpdateFileMarker + path + "' add at least one chunk:\n" +
					"  @@ <optional description>\n" +
					"   <context line, prefix is one space>\n" +
					"  -<line to remove>\n" +
					"  +<line to add>",
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
		Message: "'" + truncateForMsg(first) + "' is not a valid hunk header. " +
			"Each section must start with one of:\n" +
			"  '" + AddFileMarker + "<path>'    — followed by '+' lines for the new file's content\n" +
			"  '" + DeleteFileMarker + "<path>' — no body needed\n" +
			"  '" + UpdateFileMarker + "<path>' — followed by '@@ <optional description>' then ' '/+/- prefixed body lines",
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
			Message: "update hunk has no body lines. After '@@' (optional description) add at least one ' '/+/-prefixed line, e.g.:\n" +
				"  @@\n" +
				"  -<old line>\n" +
				"  +<new line>",
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
				Message: "expected '@@' context marker to start a new chunk, got: '" + truncateForMsg(lines[0]) + "'. " +
					"Each chunk after the first one in an Update File hunk needs '@@' (or '@@ <description>') as a section header before the body lines.",
			}
		}
		// First chunk may omit the @@ — start = 0.
	}

	if start >= len(lines) {
		return UpdateChunk{}, 0, &ParseError{
			Kind:       ErrInvalidHunk,
			LineNumber: lineNumber + 1,
			Message: "update hunk has no body lines after the '@@' marker. Add at least one ' '/+/- prefixed line below:\n" +
				"  @@\n" +
				"  -<old line>\n" +
				"  +<new line>",
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
					Message: "update hunk reached '" + EOFMarker + "' with no body lines. Put the ' '/+/- lines BEFORE '" + EOFMarker + "':\n" +
						"  @@\n" +
						"  -<old tail line>\n" +
						"  +<new tail line>\n" +
						"  " + EOFMarker,
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
						Message: "hunk format error: unexpected line '" + truncateForMsg(line) + "'. " +
							"Each line in an Update hunk's body must start with: ' ' (one space — context, no change), '+' (line to add), or '-' (line to remove). " +
							"The '@@ <description>' line is a section header, not part of the body — body lines come AFTER it.",
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
