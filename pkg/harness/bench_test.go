package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/Vignesh-Rajarajan/golum/pkg/harness/session"
	"github.com/Vignesh-Rajarajan/golum/pkg/tool"
)

func BenchmarkValidateRecordLog(b *testing.B) {
	records := make([]session.Record, 1000)
	for i := range records {
		records[i] = started(int64(i+1), "run")
		if i > 0 {
			records[i] = rec(int64(i+1), session.RecordUsage)
			records[i].RunID = "run"
		}
	}
	in := RecordLogSlice{Records: records}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = ValidateRecordLog(in)
	}
}

func BenchmarkReduceLaneState(b *testing.B) {
	records := []session.Record{started(1, "run")}
	for i := 2; i <= 100; i++ {
		r := rec(int64(i), session.RecordUsage)
		r.RunID = "run"
		records = append(records, r)
	}
	in := ReductionInput{Lane: "main", Records: records}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ReduceLaneState(in)
	}
}

func BenchmarkToolRegistryLookup(b *testing.B) {
	reg, _ := tool.DefaultRegistry(nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = reg.Get("read_file")
	}
}

func BenchmarkTruncateResult(b *testing.B) {
	s := strings.Repeat("A", 10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = truncateResult(s, 200)
	}
}

func BenchmarkSpillAndBound(b *testing.B) {
	full := strings.Repeat("A", 5000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = spillAndBound(context.Background(), nil, tool.Result{Content: full}, 200, "id")
	}
}
