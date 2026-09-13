package harness

import (
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/llm"
)

func TestPropertyTruncationNeverExceedsAndStaysUTF8(t *testing.T) {
	input := strings.Repeat("界", 80) + "tail"
	for max := 1; max < 90; max++ {
		got := truncateResult(input, max)
		if len(got) > max {
			t.Fatalf("max=%d got %d", max, len(got))
		}
		if !utf8.ValidString(got) {
			t.Fatalf("invalid utf8 at max=%d", max)
		}
	}
}

func TestPropertyToolCallHashKeyOrderIndependent(t *testing.T) {
	a := &llm.ToolCall{Name: "read_file", Arguments: map[string]any{"path": "a", "offset": 1}}
	b := &llm.ToolCall{Name: "read_file", Arguments: map[string]any{"offset": 1, "path": "a"}}
	if toolCallHash(a) != toolCallHash(b) {
		t.Fatal("hash depends on key order")
	}
}

func TestPropertyProvisionedMatchStableUnderKeyReorder(t *testing.T) {
	p := session.ProvisionedEntry{
		ID: "e", Kind: session.EntryUserMessage, Role: "user", Content: "hi",
		Meta: map[string]any{"b": 2, "a": 1},
	}
	raw, _ := json.Marshal(map[string]any{"a": 1, "b": 2})
	var meta map[string]any
	_ = json.Unmarshal(raw, &meta)
	existing := session.Entry{ID: "e", Kind: session.EntryUserMessage, Role: "user", Content: "hi", Meta: meta}
	if !p.Matches(existing) {
		t.Fatal("key reorder should not break provisioned matching")
	}
}

func TestPropertyRecordRoundTrip(t *testing.T) {
	in := session.Record{
		ID: "r1", Lane: "main", Type: session.RecordOperationFinished,
		RunID: "run", Outcome: "completed",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out session.Record
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.ID != in.ID || out.Outcome != in.Outcome {
		t.Fatalf("%+v", out)
	}
}

func TestPropertyArtifactNamesStayInsideDir(t *testing.T) {
	for _, id := range []string{"../x", "a/b", "ok_1", "weird id!", "", "..", "../../etc/passwd"} {
		got := artifactRelPath(id)
		if !strings.HasPrefix(got, artifactDir+"/") || strings.Contains(got, "..") {
			t.Fatalf("id %q -> %q", id, got)
		}
		if strings.Contains(got, "/") && strings.Count(got, "/") != strings.Count(artifactDir, "/")+1 {
			t.Fatalf("extra path segments: %q", got)
		}
	}
}

func FuzzSessionRecordJSON(f *testing.F) {
	f.Add(`{"id":"r1","lane":"main","type":"operation_started","run_id":"run","seq":1}`)
	f.Fuzz(func(t *testing.T, raw string) {
		var rec session.Record
		if json.Unmarshal([]byte(raw), &rec) != nil {
			return
		}
		out, err := json.Marshal(rec)
		if err != nil {
			t.Fatal(err)
		}
		var again session.Record
		if json.Unmarshal(out, &again) != nil {
			t.Fatal("round-trip unmarshal failed")
		}
	})
}

func FuzzToolArguments(f *testing.F) {
	f.Add(`{"path":"a.txt"}`)
	f.Add(`{`)
	f.Fuzz(func(t *testing.T, raw string) {
		tc := &llm.ToolCall{Name: "read_file", RawArguments: raw}
		_ = json.Unmarshal([]byte(raw), &tc.Arguments)
		_ = toolCallHash(tc)
	})
}

func FuzzTruncateResult(f *testing.F) {
	f.Add("hello", 3)
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 0 {
			max = -max
		}
		got := truncateResult(s, max)
		if max > 0 && len(got) > max {
			t.Fatalf("len=%d max=%d", len(got), max)
		}
		if !utf8.ValidString(got) {
			t.Fatal("invalid utf8")
		}
	})
}
