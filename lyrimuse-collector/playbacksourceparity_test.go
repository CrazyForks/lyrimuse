package main

import (
	"strings"
	"testing"
)

// App 侧播放源加固里跟 collector 同一套判据的那几处:最近记录只有一条时的对象形状、YT Music 探针的专辑名引号、
// 多标签页时暂停的那页排后。

// Last.fm 的 recenttracks.track 只有一条时是对象不是数组:照样解出这一条,不整份解失败(feed 永远写不出来)。
func TestParseLastfmRecentSingleTrackObject(t *testing.T) {
	body := `{"recenttracks":{"@attr":{"total":"1"},"track":{"name":"唯一一条","artist":{"#text":"A"},"album":{"#text":""},"date":{"uts":"1790000000"}}}}`
	page, err := parseLastfmRecent([]byte(body))
	if err != nil {
		t.Fatalf("单条对象不该解失败: %v", err)
	}
	if len(page.Done) != 1 || page.Done[0].Title != "唯一一条" || page.Total != 1 {
		t.Fatalf("应解出这一条: %+v", page)
	}
	if _, err := parseLastfmRecent([]byte(`{"recenttracks":{"@attr":{"total":"0"},"track":[]}}`)); err != nil {
		t.Fatalf("空数组照常: %v", err)
	}
}

// 专辑名里有双引号:只脱两头一对,里面被 Chromium 转义的 `\"` 还原(跟 Swift 侧 YouTubeMusicAdProbe.parse 一致)。
func TestParseYTMusicAdProbeQuotedAlbum(t *testing.T) {
	raw := `"0|0|0||Live at \"Budokan\""`
	verdict, album := parseYTMusicAdProbe(raw)
	if verdict == ytmusicAdUnknown || album != `Live at "Budokan"` {
		t.Fatalf("verdict=%v album=%q", verdict, album)
	}
	if _, album := parseYTMusicAdProbe(`0|0|0||Title"`); album != `Title"` {
		t.Errorf("没被包引号时末尾的引号要留着,got %q", album)
	}
}

// 多标签页:暂停的那页(JS 给结果加 PAUSED: 前缀)先记成备选,都找完了才交回;数标签页那一步包在 try 里。
func TestYTMusicAdAppleScriptPrefersPlayingTab(t *testing.T) {
	if !strings.Contains(ytmusicAdProbeJS, "PAUSED:") {
		t.Fatal("探针 JS 要给暂停的标签页加 PAUSED: 前缀")
	}
	for _, family := range []string{"chromium", "safari"} {
		s := buildYTMusicAdAppleScript("com.google.Chrome", family)
		for _, want := range []string{`if r starts with "PAUSED:" then`, `if fallback is not "" then return fallback`,
			"\t\ttry\n\t\t\tset tabCount to count of tabs of window wi\n\t\ton error\n"} {
			if !strings.Contains(s, want) {
				t.Errorf("%s: AppleScript 缺 %q", family, want)
			}
		}
	}
}
