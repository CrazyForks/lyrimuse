package main

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Amazon Music 放播客:歌手、专辑空,时长 > 0,在放 —— 非歌曲内容;歌手不晚到,开播那一帧照常采纳。
func TestAmazonMusicArtistlessContent(t *testing.T) {
	podcast := map[string]any{"artist": "", "album": "", "title": "Fan Favorite", "duration": 2246.0, "playing": true}
	if !builtinArtistlessContent(amazonMusicBundleID, podcast) {
		t.Error("Amazon Music 的播客应当判成非歌曲内容")
	}
	song := map[string]any{"artist": "Shakira & Burna Boy", "album": "Dai Dai", "duration": 223.0, "playing": true}
	if builtinArtistlessContent(amazonMusicBundleID, song) {
		t.Error("有歌手的是歌")
	}
	paused := map[string]any{"artist": "", "album": "", "duration": 2246.0, "playing": false}
	if builtinArtistlessContent(amazonMusicBundleID, paused) {
		t.Error("没在放的不走这条")
	}
	if trustedPlaybackNotASong(amazonMusicBundleID, "", "") {
		t.Error("Amazon Music 歌手不晚到,不该当成开播那一帧")
	}
	if builtinArtistNotReady(amazonMusicBundleID, podcast) {
		t.Error("播客不是「还没准备好」")
	}
	// 只管 artistlessNotMusic 的播放器(决策 41)。
	if builtinArtistlessContent(kugouMusicBundleID, podcast) {
		t.Error("别的内置播放器不受影响")
	}
	// KKBOX 的判定不变。
	if !builtinArtistlessContent(kkboxBundleID, podcast) {
		t.Error("KKBOX 的播客照旧判成非歌曲内容")
	}
}

func TestAmazonMusicArtistlessNotEnriched(t *testing.T) {
	if got := trackEnrichment("", "Fan Favorite", "", amazonMusicBundleID, 2246, true, false); got != nil {
		t.Errorf("Amazon Music 歌手空的内容不该解析: %v", got)
	}
}

// 信任列表里的 Amazon Music 升级后补进选中集合,并剔出信任列表。
func TestPromoteTrustedAmazonMusic(t *testing.T) {
	trusted := map[string]string{amazonMusicBundleID: "Amazon Music", "com.apple.Safari": "Safari"}
	got := promoteTrustedBuiltins(map[string]bool{playerAppleMusic: true}, trusted)
	if want := map[string]bool{playerAppleMusic: true, playerAmazonMusic: true}; !reflect.DeepEqual(got, want) {
		t.Errorf("没勾自动识别: got %v want %v", got, want)
	}
	if tp := resolveTrustedPlayers(trusted); tp[amazonMusicBundleID] != "" || tp["com.apple.Safari"] != "Safari" {
		t.Errorf("Amazon Music 内置之后剔出信任列表: %v", tp)
	}
}

func amazonAt(hhmmss string, frac float64) time.Time {
	t, _ := amazonLineTime("260928:" + hhmmss)
	return t.Add(time.Duration(frac * float64(time.Second)))
}

func amazonNear(a, b float64) bool { return math.Abs(a-b) < 0.001 }

func TestParseAmazonLogLine(t *testing.T) {
	cases := []struct {
		line string
		want amazonEvent
		ok   bool
	}{
		{"260928:025131      Browser INFO in Harley : DT:M [TrackPreFetcher.cpp:74] new track playing : asin://B0TESTAAA1:13:87015",
			amazonEvent{kind: amazonTrackStarted, trackID: "asin://B0TESTAAA1"}, true},
		{"260928:030035      Browser INFO in Harley : DT:M [TrackPreFetcher.cpp:74] new track playing : podcast://dts.podtrac.com:443/redirect.mp3/x.mp3",
			amazonEvent{kind: amazonTrackStarted, trackID: "podcast://dts.podtrac.com:443/redirect.mp3/x.mp3"}, true},
		{"260928:025218      Browser INFO in Harley : 0x3151c8000 [PlaybackEngine.cpp:1127] setPaused(1)", amazonEvent{kind: amazonPaused}, true},
		{"260928:025218 MorphoBrowser : I HarleyPlayerController : PlayerFlow : PausingPlayer : function = setPaused , paused = true : line 391, ", amazonEvent{}, false},
		{"260928:025456      Browser INFO in HarleyPlayerController : PlayerFlow line 878, function seek : Seeking to: 127990",
			amazonEvent{kind: amazonSeek, seekTo: 127.99}, true},
		{"260928:025456      Browser INFO in Harley : 0x3151c8000 [PlaybackEngine.cpp:1193] seek ( id: 14 uri: asin://B0X, seek_time: 127990 )", amazonEvent{}, false},
		{"260928:025131      Browser INFO in PlaybackListener line 255, function playbackStalled : Received callback with stalled state: 1 track_id: 13", amazonEvent{kind: amazonStallOn}, true},
		{"[SystemInfo]", amazonEvent{}, false},
		{"260928:025132      Browser INFO in Harley : DT:M [DASHRangeFragmentLoader.cpp:100] Fetching fragment: <Track: asin://B0TESTAAA1:13:87015>", amazonEvent{}, false},
	}
	for _, c := range cases {
		_, got, ok := parseAmazonLogLine(c.line)
		if ok != c.ok || got != c.want {
			t.Errorf("%q: got %+v ok=%v, want %+v ok=%v", c.line[:40], got, ok, c.want, c.ok)
		}
	}
	if at, _, _ := parseAmazonLogLine(cases[0].line); !at.Equal(amazonAt("025131", 0)) {
		t.Errorf("行首时刻按 UTC 解析: %v", at)
	}
}

// 整份样例重放,断言跟 Swift 侧 AmazonMusicTests 同一组数。
func TestAmazonPlayheadReplaySample(t *testing.T) {
	data, err := os.ReadFile("../shared/testdata/amazonmusic.log")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(data), "\n")
	replay := func(end time.Time) amazonPlayheadState {
		var s amazonPlayheadState
		for _, line := range lines {
			at, ev, ok := parseAmazonLogLine(line)
			if !ok {
				continue
			}
			when := at.Add(amazonReplayedLineOffset)
			if when.After(end) {
				break
			}
			s = applyAmazonEvent(s, ev, when)
		}
		return s
	}
	checks := []struct {
		at    string
		track string
		pos   float64
	}{
		{"025200", "asin://B0TESTAAA1", 25.5},
		{"025220", "asin://B0TESTAAA1", 44.0},
		{"025300", "asin://B0TESTAAA1", 81.5},
		{"025400", "asin://B0TESTAAA2", 22.5},
		{"025500", "asin://B0TESTAAA2", 130.49},
		{"025700", "asin://B0TESTAAA3", 41.5},
		{"030100", "podcast://dts.podtrac.com:443/redirect.mp3/example.com/episode.mp3", 21.5},
	}
	for _, c := range checks {
		s := replay(amazonAt(c.at, 0))
		if s.trackID != c.track || !amazonNear(amazonPosition(s, amazonAt(c.at, 0)), c.pos) {
			t.Errorf("%s: track=%s pos=%.3f, want %s %.3f", c.at, s.trackID, amazonPosition(s, amazonAt(c.at, 0)), c.track, c.pos)
		}
	}

	s := replay(amazonAt("025200", 0))
	md := amazonAt("025134", 0.872885)
	if got := amazonPosition(calibrateAmazonPlayhead(s, md), amazonAt("025200", 0)); !amazonNear(got, 25.127115) {
		t.Errorf("开播校准: %.6f", got)
	}
	paused := replay(amazonAt("025300", 0))
	if calibrateAmazonPlayhead(paused, md) != paused {
		t.Error("暂停过的不校准")
	}
	if !amazonMetadataStale(s, amazonAt("024343", 0)) || amazonMetadataStale(s, md) || amazonMetadataStale(paused, md) {
		t.Error("陈旧元数据判定")
	}
	if !amazonLogCovers(s, md) || amazonLogCovers(s, amazonAt("030000", 0)) {
		t.Error("日志覆盖判定")
	}
	timer := amazonSelfTimer{trackKey: "k", since: md}
	if r := amazonReadingFor(s, true, timer, md, amazonAt("025200", 0)); !r.fromLog || !amazonNear(r.position, 25.127115) {
		t.Errorf("选层:日志 %+v", r)
	}
	if r := amazonReadingFor(s, true, timer, amazonAt("024343", 0), amazonAt("025200", 0)); !r.staleMetadata {
		t.Errorf("选层:旧曲目 %+v", r)
	}
	if r := amazonReadingFor(s, false, timer, md, amazonAt("025200", 0)); r.fromLog || !amazonNear(r.position, 25.127115) {
		t.Errorf("选层:自记时 %+v", r)
	}
}

func TestAmazonSeekWaitsForStarting(t *testing.T) {
	s := applyAmazonEvent(amazonPlayheadState{}, amazonEvent{kind: amazonTrackStarted, trackID: "asin://B0TESTAAA1"}, amazonAt("030000", 0))
	s = applyAmazonEvent(s, amazonEvent{kind: amazonSeek, seekTo: 60}, amazonAt("030010", 0))
	if !amazonNear(amazonPosition(s, amazonAt("030011", 0)), 60) {
		t.Error("等 kStarting 期间停着")
	}
	if !amazonNear(amazonPosition(s, amazonAt("030015", 0)), 63) {
		t.Error("等不到就按拖动 + 2 秒起表")
	}
	paused := applyAmazonEvent(applyAmazonEvent(s, amazonEvent{kind: amazonPaused}, amazonAt("030005", 0)),
		amazonEvent{kind: amazonSeek, seekTo: 60}, amazonAt("030010", 0))
	if !amazonNear(amazonPosition(paused, amazonAt("030030", 0)), 60) {
		t.Error("暂停着拖动不起表")
	}
	started := applyAmazonEvent(s, amazonEvent{kind: amazonStarting}, amazonAt("030011", 0.4))
	if !amazonNear(amazonPosition(started, amazonAt("030012", 0.4)), 61) {
		t.Error("kStarting 那一刻起表")
	}
	mid := applyAmazonEvent(s, amazonEvent{kind: amazonSeek, seekTo: 127.99}, amazonAt("025456", 0.5))
	if !amazonNear(amazonPosition(mid, amazonAt("025456", 0.9)), 127.99) {
		t.Error("拖动后没出声前停在目标位置")
	}
}

func TestAmazonSelfTimer(t *testing.T) {
	t0 := amazonAt("025134", 0)
	timer := advanceAmazonSelfTimer(amazonSelfTimer{}, "a", t0, true, amazonAt("025136", 0))
	if !amazonNear(timer.position(amazonAt("025200", 0)), 26) {
		t.Errorf("从系统时间戳起算: %v", timer.position(amazonAt("025200", 0)))
	}
	timer = advanceAmazonSelfTimer(timer, "a", t0, false, amazonAt("025218", 0))
	if !amazonNear(timer.position(amazonAt("025230", 0)), 44) {
		t.Error("暂停冻结")
	}
	timer = advanceAmazonSelfTimer(timer, "a", t0, true, amazonAt("025222", 0))
	if !amazonNear(timer.position(amazonAt("025300", 0)), 82) {
		t.Error("恢复后扣掉暂停")
	}
	timer = advanceAmazonSelfTimer(timer, "b", amazonAt("025338", 0), true, amazonAt("025339", 0))
	if !amazonNear(timer.position(amazonAt("025400", 0)), 22) {
		t.Error("换歌从头起")
	}
	paused := advanceAmazonSelfTimer(amazonSelfTimer{}, "c", amazonAt("025000", 0), false, amazonAt("025010", 0))
	if !amazonNear(paused.position(amazonAt("025100", 0)), 10) {
		t.Error("第一次见到就是暂停着的")
	}
}

// 增量读:第一次读整份,之后只读新写的行;文件变短从头读。
func TestAmazonLogTail(t *testing.T) {
	data, err := os.ReadFile("../shared/testdata/amazonmusic.log")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "AmazonMusic.log")
	cut := strings.Index(string(data), "260928:025337")
	if err := os.WriteFile(path, data[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	tail := &amazonLogTail{path: path}
	tail.poll()
	if !tail.ok || !tail.seen || tail.state.trackID != "asin://B0TESTAAA1" {
		t.Fatalf("第一次读: %+v", tail.state)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	f.Write(data[cut:])
	f.Close()
	tail.poll()
	if tail.state.trackID != "asin://B0TESTAAA3" && !strings.HasPrefix(tail.state.trackID, "podcast://") {
		t.Fatalf("追加之后: %+v", tail.state)
	}
	if err := os.WriteFile(path, []byte("260928:040000      Browser INFO in Harley : DT:M [TrackPreFetcher.cpp:74] new track playing : asin://B0TESTBBB1:1:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	tail.poll()
	if tail.state.trackID != "asin://B0TESTBBB1" {
		t.Fatalf("文件变短从头读: %+v", tail.state)
	}
	missing := &amazonLogTail{path: filepath.Join(t.TempDir(), "none.log")}
	missing.poll()
	if missing.ok {
		t.Error("读不到文件时 ok=false")
	}
}

func TestAmazonTrackURL(t *testing.T) {
	if got := amazonTrackURL("asin://B0H9LD5H83"); got != "https://music.amazon.com/tracks/B0H9LD5H83" {
		t.Errorf("ASIN 曲目页: %q", got)
	}
	for _, bad := range []string{"podcast://x/y.mp3", "asin://B0H9LD5H8", "asin://b0h9ld5h83", "asin://B0H9LD5H83X", ""} {
		if got := amazonTrackURL(bad); got != "" {
			t.Errorf("%q 不该给链接: %q", bad, got)
		}
	}
	amazonClockMu.Lock()
	saved := amazonCurrentTrack
	amazonCurrentTrack.artist, amazonCurrentTrack.title, amazonCurrentTrack.trackID = "Kane Brown", "Boots", "asin://B0H9LD5H83"
	amazonClockMu.Unlock()
	defer func() { amazonClockMu.Lock(); amazonCurrentTrack = saved; amazonClockMu.Unlock() }()
	if got := amazonTrackURLFor(amazonMusicBundleID, "Kane Brown", "Boots"); got == "" {
		t.Error("当前这首给曲目页")
	}
	if got := amazonTrackURLFor(amazonMusicBundleID, "Kane Brown", "Other"); got != "" {
		t.Error("不是当前这首不给")
	}
	if got := amazonTrackURLFor(spotifyBundleID, "Kane Brown", "Boots"); got != "" {
		t.Error("别的播放器不给")
	}
}

// 快照 → 时钟:开播时刻取 MetadataTS(系统元数据的原始时间戳),不是 McTS(这一拍读到的时刻)。
func TestApplyAmazonMusicClockUsesMetadataTimestamp(t *testing.T) {
	data, err := os.ReadFile("../shared/testdata/amazonmusic.log")
	if err != nil {
		t.Fatal(err)
	}
	cut := strings.Index(string(data), "260928:025218")
	path := filepath.Join(t.TempDir(), "AmazonMusic.log")
	if err := os.WriteFile(path, data[:cut], 0o644); err != nil {
		t.Fatal(err)
	}
	savedPath := amazonMusicLogOverride
	amazonMusicLogOverride = path
	amazonClockMu.Lock()
	savedTail, savedTimer, savedCur := amazonClockTail, amazonClockTimer, amazonCurrentTrack
	amazonClockTail, amazonClockTimer = nil, amazonSelfTimer{}
	amazonClockMu.Unlock()
	t.Cleanup(func() {
		amazonMusicLogOverride = savedPath
		amazonClockMu.Lock()
		amazonClockTail, amazonClockTimer, amazonCurrentTrack = savedTail, savedTimer, savedCur
		amazonClockMu.Unlock()
	})

	now := amazonAt("025200", 0)
	s := snapshot{Bundle: amazonMusicBundleID, Title: "Patient Zero", Artist: "Taylor Swift", Playing: true,
		McTS: now, MetadataTS: amazonAt("025134", 0.872885)}
	if !applyAmazonMusicClock(&s, now) {
		t.Fatal("正常快照应当采纳")
	}
	if !amazonNear(s.Elapsed, 25.127115) || !s.McTS.Equal(now) {
		t.Errorf("位置按日志 + 系统时间戳校准: elapsed=%.6f", s.Elapsed)
	}
	again := s
	applyAmazonMusicClock(&again, now.Add(time.Second))
	amazonClockMu.Lock()
	since := amazonClockTail.state.since
	amazonClockMu.Unlock()
	if !since.Equal(s.MetadataTS) {
		t.Errorf("起点换成系统时间戳要记进状态,之后的暂停按它记停表位置: since=%v", since)
	}
	if got := amazonTrackURLFor(amazonMusicBundleID, "Taylor Swift", "Patient Zero"); got != "https://music.amazon.com/tracks/B0TESTAAA1" {
		t.Errorf("当前这首的曲目页: %q", got)
	}
	stale := snapshot{Bundle: amazonMusicBundleID, Title: "Choosin' Texas", Artist: "Ella Langley", Playing: true,
		McTS: now, MetadataTS: amazonAt("024343", 0)}
	if applyAmazonMusicClock(&stale, now) {
		t.Error("上一次会话留下的旧曲目不采纳")
	}
	// poller 把它当读空(连着几拍就清掉当前曲目),而不是按住上一首不放。
	staleState := map[string]any{"bundleIdentifier": amazonMusicBundleID, "title": "Choosin' Texas", "artist": "Ella Langley",
		"playing": true, "metadataTimestamp": amazonAt("024343", 0).Format(time.RFC3339)}
	if !amazonStateIsStale(staleState) {
		t.Error("旧会话残留的那份状态要认出来,poller 当读空")
	}
	freshState := map[string]any{"bundleIdentifier": amazonMusicBundleID, "title": "Patient Zero", "artist": "Taylor Swift",
		"playing": true, "metadataTimestamp": amazonAt("025134", 0.872885).Format(time.RFC3339Nano)}
	if amazonStateIsStale(freshState) {
		t.Error("这次会话的正常快照照常采纳")
	}
	if amazonStateIsStale(map[string]any{"bundleIdentifier": spotifyBundleID, "title": "x", "metadataTimestamp": amazonAt("024343", 0).Format(time.RFC3339)}) {
		t.Error("只管 Amazon Music")
	}
}

// 自然连播:End of stream 紧跟着开播的那首标成 startedNaturally;拖动后作废;App 校准的提前量只对同一次开播生效。
func TestAmazonNaturalAdvanceLead(t *testing.T) {
	s := applyAmazonEvent(amazonPlayheadState{}, amazonEvent{kind: amazonEndOfStream}, amazonAt("025618", 0.5))
	s = applyAmazonEvent(s, amazonEvent{kind: amazonTrackStarted, trackID: "asin://B0TESTAAA3"}, amazonAt("025618", 0.5))
	if !s.startedNaturally {
		t.Fatal("End of stream 紧跟着开播 = 自然连播")
	}
	clicked := applyAmazonEvent(amazonPlayheadState{}, amazonEvent{kind: amazonTrackStarted, trackID: "asin://B0TESTAAA1"}, amazonAt("025131", 0.5))
	if clicked.startedNaturally {
		t.Error("点播开头不算")
	}
	late := applyAmazonEvent(amazonPlayheadState{}, amazonEvent{kind: amazonEndOfStream}, amazonAt("025600", 0))
	late = applyAmazonEvent(late, amazonEvent{kind: amazonTrackStarted, trackID: "asin://B0TESTAAA3"}, amazonAt("025618", 0.5))
	if late.startedNaturally {
		t.Error("读完上一首隔了十几秒才开播(中间缓冲或换了列表)不算自然连播")
	}
	if got := amazonLeadApplies(amazonLeadRecord{TrackID: "asin://B0OTHER000", StartedAtMs: amazonAt("025618", 0.2).UnixMilli(), LeadSecs: 2.8}, s); got != 0 {
		t.Error("别的曲目的提前量不扣")
	}
	rec := amazonLeadRecord{TrackID: "asin://B0TESTAAA3", StartedAtMs: amazonAt("025618", 0.2).UnixMilli(), LeadSecs: 2.8}
	if got := amazonLeadApplies(rec, s); got != 2.8 {
		t.Errorf("同一次开播扣提前量: %v", got)
	}
	rec.StartedAtMs = amazonAt("030000", 0).UnixMilli()
	if got := amazonLeadApplies(rec, s); got != 0 {
		t.Error("不是这一次开播不扣")
	}
	seeked := applyAmazonEvent(s, amazonEvent{kind: amazonSeek, seekTo: 30}, amazonAt("025700", 0))
	if seeked.startedNaturally {
		t.Error("拖动之后缓冲清空,不再按自然连播扣")
	}
	rec = amazonLeadRecord{TrackID: "asin://B0TESTAAA3", StartedAtMs: amazonAt("025618", 0.2).UnixMilli(), LeadSecs: 2.8,
		WrittenAtMs: amazonAt("025630", 0).UnixMilli()}
	if got := amazonLeadApplies(rec, seeked); got != 0 {
		t.Error("拖动之前写的提前量作废")
	}
	rec.WrittenAtMs, rec.LeadSecs = amazonAt("025710", 0).UnixMilli(), -1.2
	if got := amazonLeadApplies(rec, seeked); got != -1.2 {
		t.Errorf("拖动之后卡顿重新校准的(可以是负的)照用: %v", got)
	}
	paused := applyAmazonEvent(s, amazonEvent{kind: amazonPaused}, amazonAt("025700", 0))
	resumed := applyAmazonEvent(paused, amazonEvent{kind: amazonResumed}, amazonAt("025705", 0))
	rec.WrittenAtMs, rec.LeadSecs = amazonAt("025630", 0).UnixMilli(), 2.8
	if got := amazonLeadApplies(rec, paused); got != 2.8 {
		t.Errorf("暂停着提前量照扣: %v", got)
	}
	if got := amazonLeadApplies(rec, resumed); got != 2.8 {
		t.Errorf("暂停不清缓冲,恢复后提前量照扣: %v", got)
	}
	if _, ev, ok := parseAmazonLogLine("260928:025618      Browser INFO in Harley : DT:M [Filter.cpp:157] End of stream reached"); !ok || ev.kind != amazonEndOfStream {
		t.Error("认出 End of stream")
	}
}

// Amazon 的位置是自己算的干净时钟:稳定播放时读数一变(界面校准改了提前量)就一步对齐,但不套 KKBOX 专属的规则。
func TestAmazonSnapsToReading(t *testing.T) {
	if !snapsToReading(amazonMusicBundleID) || followsRepublishedAnchors(amazonMusicBundleID) {
		t.Error("Amazon 对齐读数,但不算重发锚点的播放器")
	}
	if !snapsToReading(kkboxBundleID) || snapsToReading(spotifyBundleID) {
		t.Error("KKBOX 照旧对齐,Spotify 不对齐")
	}
}

// 开播后暂停得久,开播行会被推到末尾那段之前:第一次读至少从最后一次开播(连同它前面的 End of stream)读起。
func TestAmazonReplayStart(t *testing.T) {
	start := "260928:122914      Browser INFO in Harley : DT:M [Filter.cpp:157] End of stream reached\n" +
		"260928:122914      Browser INFO in Harley : DT:M [TrackPreFetcher.cpp:74] new track playing : asin://B0TESTAAA1:286:87015\n"
	filler := strings.Repeat("260928:123000      Browser INFO in Harley : DT:M idle while paused\n", 400)
	data := []byte(strings.Repeat("260928:120000 earlier line\n", 200) + start + filler)
	got := amazonReplayStart(data, 1000)
	if !strings.Contains(string(data[got:]), "new track playing") || !strings.Contains(string(data[got:]), "End of stream") {
		t.Fatalf("开播行在末尾那段之前,要往前读到它(连同 End of stream): start=%d", got)
	}
	if got > 0 && data[got-1] != '\n' {
		t.Error("从一行的开头读起")
	}
	recent := []byte(string(data) + start + "260928:130000 after\n")
	if got := amazonReplayStart(recent, 1000); got != len(recent)-1000 {
		t.Errorf("末尾那段里就有开播,照旧只读末尾: %d", got)
	}
	if got := amazonReplayStart([]byte(filler), 1000); got != len(filler)-1000 {
		t.Errorf("整份日志都没有开播,照旧只读末尾: %d", got)
	}
}

// App 按界面给自记时对的表:同一首、自记时在走、记录新鲜且没用过才用,位置 = 记录里的 + 写下之后过去的时间。
func TestAmazonTimerFromRecord(t *testing.T) {
	now := amazonAt("030000", 0)
	running := amazonSelfTimer{trackKey: "Beautiful Things|Benson Boone|X", base: 3300, since: amazonAt("020000", 0)}
	rec := amazonLeadRecord{WrittenAtMs: amazonAt("025958", 0).UnixMilli(), Artist: "Benson Boone", Title: "Beautiful Things", PositionSecs: 42}
	got, ok := amazonTimerFromRecord(running, rec, "Benson Boone", "Beautiful Things", 0, now)
	if !ok || !amazonNear(got.position(now), 44) {
		t.Fatalf("按界面对表: ok=%v pos=%.2f", ok, got.position(now))
	}
	if _, ok := amazonTimerFromRecord(running, rec, "Benson Boone", "Other Song", 0, now); ok {
		t.Error("别的歌不用")
	}
	if _, ok := amazonTimerFromRecord(running, rec, "Benson Boone", "Beautiful Things", rec.WrittenAtMs, now); ok {
		t.Error("同一条只用一次")
	}
	if _, ok := amazonTimerFromRecord(running, rec, "Benson Boone", "Beautiful Things", 0, now.Add(2*time.Minute)); ok {
		t.Error("太旧不用")
	}
	paused := running
	paused.since = time.Time{}
	if _, ok := amazonTimerFromRecord(paused, rec, "Benson Boone", "Beautiful Things", 0, now); ok {
		t.Error("自记时停着(暂停)不用:记录是放着的时候量的")
	}
	if got := amazonLeadApplies(rec, amazonPlayheadState{trackID: "asin://B0TESTAAA1"}); got != 0 {
		t.Error("自记时那种记录不当提前量用")
	}
}
