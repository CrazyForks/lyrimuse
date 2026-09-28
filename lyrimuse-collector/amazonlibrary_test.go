package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// 值跟目录缓存一样是 boost 归档,前缀照真实数据的形状。
const amazonTestLyricsJSON = "\x16\x00\x00\x00\x00\x00\x00\x00serialization::archive\x0f\x00\x04\x08\x04\x08\x01\x00\x00\x00\xe1\x0c\x00\x00\x00\x00\x00\x00" +
	`{"lrcSource":"AMAZON_INTERNAL","lyricsResponseCode":1002,"lyricsSource":"LYRIC_FIND","lyrics":{"lines":[` +
	`{"startTime":0,"endTime":16338,"text":"..."},` +
	`{"startTime":16338,"endTime":19959,"text":"I found your lighter in my nightstand"},` +
	`{"startTime":20227,"endTime":23965,"text":"That's why I'm thinking of you, I guess"},` +
	`{"startTime":24307,"endTime":27896,"text":"What part of over don't I understand?"}]},` +
	`"trackAsinAndMarketplace":{"asin":"B0TESTAAA2","marketplaceId":"ATVPDKIKX0DER"}}`

// 目录缓存的值是 boost 归档,中间夹一个 JSON 对象,后面还跟着二进制尾巴。
const amazonTestCatalogValue = "\x16\x00\x00\x00\x00\x00\x00\x00serialization::archive\x13\x00\x04\x08\x04\x08\x01\x00\x00\x00" +
	`{"id":"","uniqueId":"B0TESTAAA2","asin":"B0TESTAAA2","title":"I Can't Love You Anymore [Explicit]","duration":"229","hasLyrics":true,` +
	`"album":{"name":"I Can't Love You Anymore","asin":"B0TESTALB1"},"artist":{"name":"Ella Langley & Morgan Wallen","asin":"B0TESTART1"}}` +
	"\xe5\x00\x01garbage"

func useTempAmazonData(t *testing.T) (localStorage, hammer string) {
	t.Helper()
	localStorage, hammer = t.TempDir(), t.TempDir()
	savedLS, savedHC := amazonLocalStorageOverride, amazonHammerCacheOverride
	amazonLocalStorageOverride, amazonHammerCacheOverride = localStorage, hammer
	t.Cleanup(func() { amazonLocalStorageOverride, amazonHammerCacheOverride = savedLS, savedHC })
	testWriteLog(t, filepath.Join(localStorage, "000015.log"), [][]testLDBEntry{{
		{key: "*.MusicContent.CacheEntry.PrimeCatalog_KATANA_B0TESTAAA2", seq: 5, value: amazonTestCatalogValue},
	}})
	testWriteLog(t, filepath.Join(hammer, "000011.log"), [][]testLDBEntry{{
		{key: "B0TESTAAA2-ATVPDKIKX0DER", seq: 7, value: amazonTestLyricsJSON},
	}})
	return localStorage, hammer
}

func TestAmazonLyricsLRC(t *testing.T) {
	lrc, coarse, ok := amazonLyricsLRC([]byte(amazonTestLyricsJSON))
	if !ok || coarse {
		t.Fatalf("ok=%v coarse=%v", ok, coarse)
	}
	if strings.Contains(lrc, "...") || !strings.HasPrefix(lrc, "[00:16.33]I found your lighter") {
		t.Errorf("前奏占位去掉、时间按毫秒换算: %q", lrc)
	}
	if _, coarse, _ := amazonLyricsLRC([]byte(`{"lyrics":{"lines":[{"startTime":1000,"text":"a"},{"startTime":2000,"text":"b"},{"startTime":3000,"text":"c"}]}}`)); !coarse {
		t.Error("每句都是整秒的标成 coarse")
	}
	if _, _, ok := amazonLyricsLRC([]byte(`{"lyrics":{"lines":[{"startTime":1500,"text":"a"}]}}`)); ok {
		t.Error("句子太少不算有歌词")
	}
}

func TestAmazonCatalogAndLyricsFromLevelDB(t *testing.T) {
	useTempAmazonData(t)
	meta := amazonCatalog([]string{"B0TESTAAA2", "B0MISSING0"})
	got, ok := meta["B0TESTAAA2"]
	if !ok || got.Title != "I Can't Love You Anymore [Explicit]" || got.Artist.Name != "Ella Langley & Morgan Wallen" ||
		got.albumName() != "I Can't Love You Anymore" || got.durationSecs() != 229 {
		t.Fatalf("目录缓存: %+v", got)
	}
	if _, ok := meta["B0MISSING0"]; ok {
		t.Error("查不到的不在结果里")
	}
	if lrc, _, ok := amazonLyricsForASIN("B0TESTAAA2"); !ok || !strings.Contains(lrc, "nightstand") {
		t.Errorf("按 ASIN 前缀找歌词: ok=%v", ok)
	}
	if _, _, ok := amazonLyricsForASIN("B0TESTAAA"); ok {
		t.Error("ASIN 前缀要带分隔符,不能半截命中")
	}
}

func TestParseAmazonQueueLine(t *testing.T) {
	q, ok := parseAmazonQueueLine("260928:025618 MorphoBrowser : I HarleyPlayerController : PlayerFlow : Playables : UriList = asin-//B0TESTAAA1, asin-//B0TESTAAA2, asin-//B0TESTAAA3 , function = updateQueue : line 571, ")
	if !ok || strings.Join(q, ",") != "asin://B0TESTAAA1,asin://B0TESTAAA2,asin://B0TESTAAA3" {
		t.Fatalf("队列窗口: %v ok=%v", q, ok)
	}
	if _, ok := parseAmazonQueueLine("260928:025618 Browser INFO in Harley : new track playing : asin://B0TESTAAA1:1:1"); ok {
		t.Error("别的行不认")
	}
}

// 队列预解析:窗口第一首要是日志里正在放的、也是播放器报的这首;交出后两首,并记下 ASIN 给歌词用。
func TestAmazonUpcomingAndLocalLyrics(t *testing.T) {
	useTempAmazonData(t)
	amazonClockMu.Lock()
	savedCur, savedTail := amazonCurrentTrack, amazonClockTail
	amazonCurrentTrack.artist, amazonCurrentTrack.title, amazonCurrentTrack.trackID = "Morgan Wallen", "Been By Now", "asin://B0TESTAAA1"
	amazonClockTail = &amazonLogTail{queue: []string{"asin://B0TESTAAA1", "asin://B0TESTAAA2", "asin://B0MISSING0"}}
	amazonClockMu.Unlock()
	t.Cleanup(func() {
		amazonClockMu.Lock()
		amazonCurrentTrack, amazonClockTail = savedCur, savedTail
		amazonClockMu.Unlock()
	})

	got, ok := amazonUpcoming("Morgan Wallen", "Been By Now", 5)
	if !ok || len(got) != 1 || got[0].title != "I Can't Love You Anymore [Explicit]" || got[0].artist != "Ella Langley & Morgan Wallen" || got[0].duration != 229 {
		t.Fatalf("交出后面那首(目录里查不到的不交): %+v ok=%v", got, ok)
	}
	if _, ok := amazonUpcoming("Someone Else", "Other", 5); ok {
		t.Error("播放器报的不是日志里那首,退回同专辑预取")
	}
	amazonClockMu.Lock()
	amazonClockTail.queue = []string{"asin://B0OTHER000", "asin://B0TESTAAA2"}
	amazonClockMu.Unlock()
	if _, ok := amazonUpcoming("Morgan Wallen", "Been By Now", 5); ok {
		t.Error("窗口第一首不是当前这首,退回")
	}

	// 队列里那首解析歌词时按记下的 ASIN 认身份(歌名是剥过 [Explicit] 的);只在正用 Amazon Music 放时读。
	setNativeLyricSourcesForPlayer(amazonMusicBundleID)
	t.Cleanup(func() { setNativeLyricSourcesForPlayer("") })
	r, ok := amazonLocalLyricsFor("Ella Langley & Morgan Wallen", "I Can't Love You Anymore")
	if !ok || r.source != amazonLocalLyricsSource || !r.identityFromLocalClient || r.srcDur != 229 || r.matchAlbum == "" {
		t.Fatalf("本地歌词: %+v ok=%v", r, ok)
	}
	setNativeLyricSourcesForPlayer(spotifyBundleID)
	if _, ok := amazonLocalLyricsFor("Ella Langley & Morgan Wallen", "I Can't Love You Anymore"); ok {
		t.Error("没在用 Amazon Music 放时不读")
	}
}

func TestAmazonLyricsWorthRecheck(t *testing.T) {
	e := enrichEntry{Lyrics: "[00:01.00]x", LyricsSourcesSeen: []string{"qq", "kugou"}}
	if !amazonLyricsWorthRecheck(e, amazonMusicBundleID, false, true, true) {
		t.Error("没见过 Amazon 那份、现在有 → 重来一次")
	}
	e.LyricsSourcesSeen = append(e.LyricsSourcesSeen, amazonLocalLyricsSource)
	if amazonLyricsWorthRecheck(e, amazonMusicBundleID, false, true, true) {
		t.Error("见过就不再来")
	}
	if amazonLyricsWorthRecheck(enrichEntry{}, kkboxBundleID, false, true, true) {
		t.Error("只管 Amazon Music")
	}
}
