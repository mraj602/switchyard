package scan

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// HistoryImport is one history_import record (record.md 4.6): the slot's stats-cache.json
// kept whole, split into parts only when it is large.
type HistoryImport struct {
	Slot   string
	Source string // "stats_cache"
	Raw    json.RawMessage
	Part   int // 1-based; 0 when not split
	Parts  int
}

// splitThreshold is the raw size above which stats-cache.json is split. A batch is at most
// 8 MiB before gzip, so stay well below.
const splitThreshold = 2 << 20

// ReadHistory reads <slot>/stats-cache.json for every slot that has a valid one.
func ReadHistory(slots []Slot) []HistoryImport {
	var out []HistoryImport
	for _, sl := range slots {
		b, err := os.ReadFile(filepath.Join(sl.Dir, "stats-cache.json"))
		if err != nil || !json.Valid(b) {
			continue
		}
		out = append(out, splitHistory(sl.Folder, b)...)
	}
	return out
}

func splitHistory(slot string, raw []byte) []HistoryImport {
	whole := []HistoryImport{{Slot: slot, Source: "stats_cache", Raw: raw}}
	if len(raw) <= splitThreshold {
		return whole
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return whole
	}
	var act, tok []json.RawMessage
	if json.Unmarshal(m["dailyActivity"], &act) != nil || json.Unmarshal(m["dailyModelTokens"], &tok) != nil {
		return whole
	}
	parts := len(raw)/splitThreshold + 1
	n := max(len(act), len(tok))
	per := (n + parts - 1) / parts
	slice := func(a []json.RawMessage, i int) []json.RawMessage {
		lo, hi := min(i*per, len(a)), min((i+1)*per, len(a))
		return a[lo:hi]
	}
	var out []HistoryImport
	for i := 0; i < parts; i++ {
		p := map[string]json.RawMessage{}
		if i == 0 {
			for k, v := range m {
				p[k] = v
			}
		} else {
			for _, k := range []string{"version", "lastComputedDate"} {
				if v, ok := m[k]; ok {
					p[k] = v
				}
			}
		}
		p["dailyActivity"], _ = json.Marshal(slice(act, i))
		p["dailyModelTokens"], _ = json.Marshal(slice(tok, i))
		b, _ := json.Marshal(p)
		out = append(out, HistoryImport{Slot: slot, Source: "stats_cache", Raw: b, Part: i + 1, Parts: parts})
	}
	return out
}
