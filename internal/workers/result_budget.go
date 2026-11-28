package workers

import (
	"encoding/json"
	"sort"
	"strconv"
	"unicode/utf8"
)

// Keep serialized workers_result data comfortably inside both
// automation.MaxResponse (64 KiB) and the MCP text projection cap (48 KiB)
// after outer response / next_step wrappers.
const resultPageMaxPayloadJSONBytes = 46 << 10

func resultPageJSONSize(p ResultPage) int {
	b, err := json.Marshal(p)
	if err != nil {
		return 0
	}
	return len(b)
}

func applyResultPageSlice(base ResultPage, rem string, take, offset, totalLen int) ResultPage {
	out := base
	out.Text = rem[:take]
	if offset+take >= totalLen {
		out.Complete = true
		out.NextCursor = ""
	} else {
		out.Complete = false
		out.NextCursor = strconv.Itoa(offset + take)
	}
	return out
}

// fitResultPageBudget bounds the serialized JSON size of page to maxBytes while
// keeping page.Text aligned to UTF-8 rune boundaries and advancing the cursor
// by the exact delivered byte count.
func fitResultPageBudget(page *ResultPage, rem string, take, offset, totalLen, maxBytes int) {
	cand := applyResultPageSlice(*page, rem, take, offset, totalLen)
	if resultPageJSONSize(cand) <= maxBytes {
		*page = cand
		return
	}

	ends := utf8CursorBoundaries(rem[:take], rem)
	if len(ends) > 0 && ends[len(ends)-1] == take {
		ends = ends[:len(ends)-1]
	}
	if len(ends) > 0 {
		firstTooLarge := sort.Search(len(ends), func(i int) bool {
			probe := applyResultPageSlice(*page, rem, ends[i], offset, totalLen)
			return resultPageJSONSize(probe) > maxBytes
		})
		if firstTooLarge > 0 {
			*page = applyResultPageSlice(*page, rem, ends[firstTooLarge-1], offset, totalLen)
			return
		}
	}

	// Always deliver at least one UTF-8 rune when rem is non-empty so callers
	// make strict forward progress even under degenerate base metadata.
	_, firstLen := utf8.DecodeRuneInString(rem)
	if firstLen <= 0 || firstLen > len(rem) {
		firstLen = len(rem)
	}
	*page = applyResultPageSlice(*page, rem, firstLen, offset, totalLen)
}
