package main

import "testing"

// resync-lyrics:源没给、本地生成的罗马音 / 机翻 / 逐字,正文没变时不算变、也不清掉。
func TestPlanResyncKeepsLocallyGeneratedFields(t *testing.T) {
	e := enrichEntry{Lyrics: "[00:01.00]词", LyricsRoma: "[00:01.00]ci", LyricsTr: "[00:01.00]lyric",
		LyricsTrSource: lyricsTrSourceMachine, LyricsYRC: "[1000,500](1000,500,0)词", LyricsSource: "qq"}
	picked := &scoredLyricCandidateResult{Source: "qq", Lyrics: e.Lyrics, Score: 800}
	p := planResync(e, picked)
	if p.changed() || !p.keepLocalRoma || !p.keepMachineTr || !p.keepYRC {
		t.Fatalf("正文没变、源只是没给这几样:不算改动: %+v", p)
	}
	got := applyResync(e, picked, p, "zh", "")
	if got.LyricsRoma != e.LyricsRoma || got.LyricsTr != e.LyricsTr || got.LyricsTrSource != lyricsTrSourceMachine || got.LyricsYRC != e.LyricsYRC {
		t.Fatalf("本地那几份应当原样留着: %+v", got)
	}

	// 源自带了新译文:照换,机翻标记清掉。
	picked2 := &scoredLyricCandidateResult{Source: "netease", Lyrics: e.Lyrics, LyricsTr: "[00:01.00]社区译文", LyricsTrLang: "zh", Score: 820}
	p2 := planResync(e, picked2)
	if p2.trSame || !p2.changed() {
		t.Fatalf("源给了不同的译文就是改动: %+v", p2)
	}
	got2 := applyResync(e, picked2, p2, "zh", "")
	if got2.LyricsTr != "[00:01.00]社区译文" || got2.LyricsTrSource != "" || got2.LyricsTrLang != "zh" {
		t.Fatalf("新译文应当换上、标记跟着换: %+v", got2)
	}

	// 正文换了:本地那几份不再对得上,按冠军的来,罗马音用锁外算好的兜底。
	picked3 := &scoredLyricCandidateResult{Source: "kugou", Lyrics: "[00:01.00]新词", Score: 900}
	p3 := planResync(e, picked3)
	if p3.lyricsSame || p3.keepLocalRoma || p3.keepMachineTr || p3.keepYRC {
		t.Fatalf("正文换了就不留旧的: %+v", p3)
	}
	got3 := applyResync(e, picked3, p3, "zh", "[00:01.00]xin ci")
	if got3.Lyrics != "[00:01.00]新词" || got3.LyricsTr != "" || got3.LyricsYRC != "" || got3.LyricsRoma != "[00:01.00]xin ci" ||
		got3.LyricsSource != "kugou" || got3.LyricsScoringVersion != lyricsScoringVersion || got3.SongLanguage != "zh" {
		t.Fatalf("got %+v", got3)
	}
}
