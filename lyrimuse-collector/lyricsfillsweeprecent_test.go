package main

import (
	"context"
	"reflect"
	"testing"
)

// 补搜的进度里带上跳过数和最近几条的结果(新的在前、最多 lyricsFillRecentMax 条);全量扫库那一轮不记最近结果。
func TestLyricsFillSweepRecentAndSkipped(t *testing.T) {
	outcomes := map[string]lyricsSweepOutcome{
		"a": {filled: true},
		"b": {},
		"c": {skipped: true},
		"d": {filled: true},
	}
	stubLyricsFillSweep(t, func(_ context.Context, key string) lyricsSweepOutcome { return outcomes[key] })
	st := runLyricsFillSweepKeys(context.Background(), []string{"a", "b", "c", "d"}, false, 0, lyricsFillStatus{Running: true, Total: 4})
	if st.Done != 4 || st.Filled != 2 || st.Skipped != 1 {
		t.Fatalf("计数: %+v", st)
	}
	want := []lyricsFillRecent{{Key: "d", Result: "filled"}, {Key: "c", Result: "skipped"}, {Key: "b", Result: "missed"}}
	if !reflect.DeepEqual(st.Recent, want) {
		t.Fatalf("最近结果 got %+v want %+v", st.Recent, want)
	}
	full := runLyricsFillSweepKeys(context.Background(), []string{"a", "b"}, true, 0, lyricsFillStatus{Running: true, Total: 2})
	if len(full.Recent) != 0 {
		t.Fatalf("全量扫库不记最近结果: %+v", full.Recent)
	}
}
