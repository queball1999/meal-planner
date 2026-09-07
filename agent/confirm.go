package agent

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Pending is a mutating tool call the assistant wants to make, held until a
// person says yes.
//
// The assistant rewrites a real household's real plan. Everything else about
// it is recoverable by asking again; a silent write is not, and "it moved
// three meals and I do not know which" is the failure mode that makes an
// assistant untrustworthy rather than merely wrong.
type Pending struct {
	Tool string          `json:"tool"`
	Args json.RawMessage `json:"args"`
	// What the tool does, from its own registered description - so the reader
	// is told the tool's purpose rather than having to infer it from a name.
	Description string `json:"description"`
	// Detail spells the arguments out in a readable line.
	Detail string `json:"detail"`
	// Resume carries the run's transcript so the conversation can continue
	// from exactly where it paused. Opaque to the caller.
	Resume string `json:"-"`
}

// describeCall renders a held call as something a person can decide about.
//
// Arguments are listed rather than prosed: a generated sentence per tool would
// be a second description to keep in step with the tool itself, and the raw
// values are what actually matter here - "day: monday" is the fact the reader
// is checking, and a fluent sentence around it adds nothing.
func describeCall(t *Tool, args json.RawMessage) string {
	var m map[string]any
	if len(args) > 0 {
		if err := json.Unmarshal(args, &m); err != nil {
			return strings.TrimSpace(string(args))
		}
	}
	if len(m) == 0 {
		return "no arguments"
	}

	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Required arguments first, then the rest alphabetically: the required
	// ones are what the call is actually about.
	sort.SliceStable(keys, func(i, j int) bool {
		ri, rj := contains(t.Required, keys[i]), contains(t.Required, keys[j])
		if ri != rj {
			return ri
		}
		return keys[i] < keys[j]
	})

	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+": "+formatArg(m[k]))
	}
	return strings.Join(parts, ", ")
}

func formatArg(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		// Every JSON number decodes as float64; ids and counts should not read
		// as "47.000000".
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case nil:
		return "(none)"
	default:
		blob, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(blob)
	}
}
