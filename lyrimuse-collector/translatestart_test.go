package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupTranslateStart 接一个假 MyMemory、打开机翻,返回的 calls 记录送翻请求数。
func setupTranslateStart(t *testing.T) *int {
	t.Helper()
	calls := new(int)
	srv := fakeMyMemory(t, func(w http.ResponseWriter, r *http.Request) {
		enrichMu.Lock()
		*calls++
		enrichMu.Unlock()
		lines := strings.Split(r.URL.Query().Get("q"), "\n")
		out := make([]string, len(lines))
		for i := range lines {
			out[i] = "译" + lines[i]
		}
		body, _ := jsonEscape(strings.Join(out, "\n"))
		fmt.Fprintf(w, `{"responseData":{"translatedText":%s},"responseStatus":200}`, body)
	})
	savedFeatures, savedCache, savedPath, savedBase, savedDir, savedClient :=
		features(), enrichCache, enrichPath, translateBaseURL, lyricsDir(), translateClient
	savedInflight, savedTr, savedProv := enrichInflight, translationInflight, enrichProvisional
	t.Cleanup(func() {
		setFeatures(savedFeatures)
		enrichCache, enrichPath, translateBaseURL, translateClient =
			savedCache, savedPath, savedBase, savedClient
		enrichInflight, translationInflight, enrichProvisional = savedInflight, savedTr, savedProv
		setLyricsDir(savedDir)
	})
	featuresRef().LyricsMachineTranslation = true
	featuresRef().LyricsTranslationLanguage = "zh"
	translateBaseURL = srv.URL
	translateClient = srv.Client()
	setLyricsDir("")
	enrichPath = filepath.Join(t.TempDir(), "enrich-cache.json")
	enrichInflight, translationInflight, enrichProvisional = map[string]bool{}, map[string]bool{}, map[string]bool{}
	return calls
}

const translateStartLyrics = "[00:01.00]The painful youth\n[00:02.00]I have had"

func waitTranslationDone(t *testing.T, key string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		enrichMu.Lock()
		busy := translationInflight[key]
		enrichMu.Unlock()
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s 的机翻 10 秒没跑完", key)
}

// 别的后台任务占着 enrichInflight 时照样起机翻,而且机翻收尾不能把别人的占位清掉。
func TestTranslationRunsAlongsideOtherBackgroundWork(t *testing.T) {
	setupTranslateStart(t)
	const key = "Someone|Some Song|Some Album"
	enrichMu.Lock()
	enrichCache = map[string]enrichEntry{key: {Lyrics: translateStartLyrics}}
	enrichInflight[key] = true // 比如重打分正在跑
	started := startTranslationBackfillLocked(key, enrichCache[key])
	again := startTranslationBackfillLocked(key, enrichCache[key])
	enrichMu.Unlock()
	if !started || again {
		t.Fatalf("started=%v again=%v:第一次该起、在途时不该重复起", started, again)
	}
	waitTranslationDone(t, key)
	enrichMu.Lock()
	defer enrichMu.Unlock()
	if enrichCache[key].LyricsTrSource != lyricsTrSourceMachine {
		t.Fatalf("没翻出来: %+v", enrichCache[key])
	}
	if !enrichInflight[key] {
		t.Fatal("机翻收尾把别的任务在 enrichInflight 里的占位清掉了")
	}
	// 同时在跑的重打分随后换了正文、译文跟着清空:下一次轮询要能马上按新正文重翻,不被节流挡住。
	e := enrichCache[key]
	e.Lyrics, e.LyricsTr, e.LyricsTrLang, e.LyricsTrSource = "[00:01.00]Another line\n[00:02.00]And one more", "", "", ""
	if !needsTranslationBackfill(e, key) {
		t.Fatalf("翻成之后换了正文,重翻被节流挡住了: ts=%d retries=%d", e.TranslationTS, e.TranslationRetryCount)
	}
}

func TestTranslateAfterResolveMark(t *testing.T) {
	if translateAfterResolve(context.Background()) || !translateAfterResolve(withBackgroundOutbound(withTranslateAfterResolve(context.Background()))) {
		t.Fatal("标记要能穿过 withBackgroundOutbound 读出来,没标的读成 false")
	}
}

// 首次解析在途(先上屏那一份)时不起:最终提交会整条覆盖,译文会被冲掉。
func TestTranslationWaitsForProvisionalCommit(t *testing.T) {
	setupTranslateStart(t)
	const key = "Someone|Some Song|Some Album"
	enrichMu.Lock()
	defer enrichMu.Unlock()
	enrichCache = map[string]enrichEntry{key: {Lyrics: translateStartLyrics}}
	enrichProvisional[key] = true
	if startTranslationBackfillLocked(key, enrichCache[key]) || translateUpcomingLocked(key) {
		t.Fatal("先上屏那一份还会被整条覆盖,不该起机翻")
	}
	delete(enrichProvisional, key)
	if !translationStartableLocked(key, enrichCache[key]) {
		t.Fatal("最终提交之后应该能起")
	}
}

// 预取来的机翻排队:槽被占着时不送翻,槽空出来才翻;翻完译文在缓存里。
func TestTranslateUpcomingQueuesBehindSlot(t *testing.T) {
	calls := setupTranslateStart(t)
	const key = "Someone|Next Song|Some Album"
	prefetchTranslateSlot <- struct{}{}
	enrichMu.Lock()
	enrichCache = map[string]enrichEntry{key: {Lyrics: translateStartLyrics}}
	queued := translateUpcomingLocked(key)
	enrichMu.Unlock()
	if !queued {
		<-prefetchTranslateSlot
		t.Fatal("该排进去")
	}
	time.Sleep(100 * time.Millisecond)
	enrichMu.Lock()
	early := *calls
	enrichMu.Unlock()
	<-prefetchTranslateSlot
	if early != 0 {
		t.Fatalf("槽被占着时已经送翻了 %d 次", early)
	}
	waitTranslationDone(t, key)
	enrichMu.Lock()
	defer enrichMu.Unlock()
	if enrichCache[key].LyricsTr == "" {
		t.Fatal("槽空出来之后没翻")
	}
}

// 接线守卫:正在播的那首的机翻不挂在「一次只跑一路」那条链里;待播预取解析完接着排机翻;
// 同专辑预取不排。
func TestTranslationStartIsWired(t *testing.T) {
	read := func(f string) string {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	enrich := read("enrich.go")
	if strings.Contains(enrich, "go backfillTranslation(") || strings.Contains(enrich, "needsTranslationBackfill(e, key) && !enrichInflight[key]") {
		t.Error("enrich.go 里机翻又挂回了 enrichInflight 那条链")
	}
	if !strings.Contains(enrich, "\t\tstartTranslationBackfillLocked(key, e)\n\t\tenrichMu.Unlock()") {
		t.Error("enrich.go 缓存命中那段没接 startTranslationBackfillLocked")
	}
	if !strings.Contains(enrich, "\t\tif translateAfterResolve(ctx) {\n\t\t\ttranslateUpcomingLocked(key)\n\t\t}\n\t\tenrichMu.Unlock()") {
		t.Error("resolveEnrichAsync 收尾没接 translateAfterResolve")
	}
	upcoming := read("upcoming.go")
	for _, needle := range []string{
		"go resolveEnrichAsync(withBackgroundOutbound(withTranslateAfterResolve(context.Background())), key,",
		"} else if exists {\n\t\t\t// 解析过、但还没译文的",
	} {
		if !strings.Contains(upcoming, needle) {
			t.Errorf("upcoming.go 缺 %q", needle)
		}
	}
	album := read("albumprefetch.go")
	if strings.Contains(album, "translateUpcomingLocked(") || strings.Contains(album, "withTranslateAfterResolve(") {
		t.Error("同专辑预取不该排机翻")
	}
}
