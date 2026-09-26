package main

import (
	"math"
	"testing"
)

// KKBOX 开播第一个锚点晚约 0.18s、之后每秒重发一次准的:头一拍读到晚的那个,下一拍读数一对齐就跟上;
// 差不到 followsAnchorSnapSecs 不动;别的播放器照旧按墙钟外推、不追读数。
func TestUpdatePositionFollowsRepublishedAnchors(t *testing.T) {
	p := &poller{}
	p.cur = snapshot{Title: "T", Artist: "A", Album: "Alb", Duration: 240, Playing: true, Elapsed: 0.19, Rate: 1,
		Bundle: kkboxBundleID, McTS: nowAt(0)}
	p.updatePosition(nowAt(0))
	p.cur.Elapsed, p.cur.McTS = 5.37, nowAt(5) // 真实位置 = 0.19 + 0.18 + 5
	reanchor, _ := p.updatePosition(nowAt(5))
	if !reanchor || math.Abs(p.trackPos-5.37) > 1e-9 {
		t.Fatalf("差 0.18s 要对齐读数并重推: reanchor=%v pos=%.3f", reanchor, p.trackPos)
	}
	p.cur.Elapsed, p.cur.McTS = 10.42, nowAt(10) // 只差 0.05s
	if reanchor, _ := p.updatePosition(nowAt(10)); reanchor || math.Abs(p.trackPos-10.37) > 1e-9 {
		t.Fatalf("差不到门槛不动: reanchor=%v pos=%.3f", reanchor, p.trackPos)
	}

	other := &poller{}
	other.cur = snapshot{Title: "T", Artist: "A", Album: "Alb", Duration: 240, Playing: true, Elapsed: 0.19, Rate: 1,
		Bundle: kugouMusicBundleID, McTS: nowAt(0)}
	other.updatePosition(nowAt(0))
	other.cur.Elapsed, other.cur.McTS = 5.37, nowAt(5)
	if reanchor, _ := other.updatePosition(nowAt(5)); reanchor || math.Abs(other.trackPos-5.19) > 1e-9 {
		t.Fatalf("别的播放器照旧外推: reanchor=%v pos=%.3f", reanchor, other.trackPos)
	}
}
