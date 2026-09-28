package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Amazon Music 的播放位置。
//
// 它在系统 Now Playing 里不报 elapsedTime:时间戳是这首真正出声(缓冲完)的时刻,暂停只翻 playing、
// playbackRate 恒为 1、时间戳不动,拖动进度什么都不报。所以位置自己算,两层:
//
//  1. 日志重放:Amazon Music 把开播、暂停、恢复、拖动(精确到毫秒的目标位置)、缓冲卡顿实时写进
//     ~/Library/Application Support/Amazon Music/Logs/AmazonMusic.log。每拍增量读新写的行,按行首时刻
//     (只到秒,取该秒 + 0.5)推进状态。拖动之后要等引擎进入 kStarting 才真正出声,表停在目标位置等这一行,
//     见 amazonSeekStartTimeout。
//  2. 自记时:日志里没有这首的开播(读不到、措辞改了),从系统时间戳起算、扣掉观察到的暂停。拖动之后会错位,
//     到下一首为止。
//
// Swift 侧 AmazonMusicPlayhead 同一套规则,两侧测试读同一份样例 shared/testdata/amazonmusic.log。
//
// 自动连播(End of stream reached 紧跟着开播)的那首,位置领先真正出声一段(上一首还在输出缓冲里没放完)。这个量只有
// App 读 Amazon 界面能校准(collector 没有辅助功能权限),App 写进 lyrimuse-amazon-lead.json,这里读到同一首就扣掉,
// 见 amazonLeadFor。

type amazonEventKind int

const (
	amazonTrackStarted amazonEventKind = iota
	amazonPaused
	amazonResumed
	amazonSeek
	amazonStallOn
	amazonStallOff
	amazonStarting
	amazonEndOfStream
)

type amazonEvent struct {
	kind    amazonEventKind
	trackID string  // amazonTrackStarted:asin://<ASIN>,播客是整段 podcast://…
	seekTo  float64 // amazonSeek:秒
}

// amazonReplayedLineOffset:行首只到秒,真实时刻落在 [该秒, 该秒 + 1),取中点。
const amazonReplayedLineOffset = 500 * time.Millisecond

// amazonCalibrationWindow / amazonStaleMetadataLead 同 Swift 侧 calibrationWindow / staleMetadataLead。
const (
	amazonCalibrationWindow  = 15 * time.Second
	amazonStaleMetadataLead  = 5 * time.Second
	amazonLogTailOnFirstRead = 256 << 10
)

// parseAmazonLogLine 解析一行日志,认不出返回 ok=false。
func parseAmazonLogLine(line string) (at time.Time, ev amazonEvent, ok bool) {
	at, ok = amazonLineTime(line)
	if !ok {
		return time.Time{}, amazonEvent{}, false
	}
	if i := strings.Index(line, "new track playing : "); i >= 0 {
		id := amazonTrackID(strings.TrimSpace(line[i+len("new track playing : "):]))
		if id == "" {
			return time.Time{}, amazonEvent{}, false
		}
		return at, amazonEvent{kind: amazonTrackStarted, trackID: id}, true
	}
	// 暂停在日志里有两行(控制层的 `setPaused , paused = true` 和引擎的 `setPaused(1)`),只认引擎那一行。
	if strings.Contains(line, "setPaused(1)") {
		return at, amazonEvent{kind: amazonPaused}, true
	}
	if strings.Contains(line, "Entering kStarting state") {
		return at, amazonEvent{kind: amazonStarting}, true
	}
	if strings.Contains(line, "End of stream reached") {
		return at, amazonEvent{kind: amazonEndOfStream}, true
	}
	if strings.Contains(line, "setPaused(0)") {
		return at, amazonEvent{kind: amazonResumed}, true
	}
	if i := strings.Index(line, "function seek : Seeking to: "); i >= 0 {
		rest := line[i+len("function seek : Seeking to: "):]
		n := 0
		for n < len(rest) && rest[n] >= '0' && rest[n] <= '9' {
			n++
		}
		ms, err := strconv.ParseFloat(rest[:n], 64)
		if err != nil {
			return time.Time{}, amazonEvent{}, false
		}
		return at, amazonEvent{kind: amazonSeek, seekTo: ms / 1000}, true
	}
	if i := strings.Index(line, "Received callback with stalled state: "); i >= 0 {
		rest := line[i+len("Received callback with stalled state: "):]
		switch {
		case strings.HasPrefix(rest, "1"):
			return at, amazonEvent{kind: amazonStallOn}, true
		case strings.HasPrefix(rest, "0"):
			return at, amazonEvent{kind: amazonStallOff}, true
		}
	}
	return time.Time{}, amazonEvent{}, false
}

// amazonTrackID:asin://B0EXAMPLE:15:87015 → asin://B0EXAMPLE;podcast://… 原样。
func amazonTrackID(uri string) string {
	if rest, ok := strings.CutPrefix(uri, "asin://"); ok {
		asin, _, _ := strings.Cut(rest, ":")
		if asin == "" {
			return ""
		}
		return "asin://" + asin
	}
	if strings.HasPrefix(uri, "podcast://") {
		return uri
	}
	return ""
}

// amazonLineTime 解析行首 `YYMMDD:HHMMSS`(UTC)。
func amazonLineTime(line string) (time.Time, bool) {
	if len(line) < 13 || line[6] != ':' {
		return time.Time{}, false
	}
	for i := 0; i < 13; i++ {
		if i != 6 && (line[i] < '0' || line[i] > '9') {
			return time.Time{}, false
		}
	}
	t, err := time.ParseInLocation("060102:150405", line[:13], time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// amazonPlayheadState 同 Swift 侧 AmazonMusicPlayhead.State。纯函数 applyAmazonEvent 只读写它的值。
type amazonPlayheadState struct {
	trackID        string
	trackStartedAt time.Time
	base           float64   // since 那一刻的位置;表停着时就是当前位置
	since          time.Time // 零值 = 停着(暂停或卡顿)
	paused         bool
	stalled        bool
	pristine       bool // 开播之后没暂停、没拖动过,见 calibrateAmazonPlayhead
	// awaitingStartUntil:拖动之后还没等到 kStarting,表停在目标位置,最晚到这一刻起表。零值 = 没在等。
	awaitingStartUntil time.Time
	lastEndOfStreamAt  time.Time
	// startedNaturally:这首是自然连播开的头,同 Swift 侧 startedNaturally。
	startedNaturally bool
	// lastFlushAt:最近一次拖动(> 0)的时刻。拖动清空缓冲,App 在那之前校准的提前量作废(见 amazonLeadApplies)。
	// 暂停不清缓冲,恢复后照用。
	lastFlushAt time.Time
}

// amazonNaturalAdvanceWindow 同 Swift 侧 naturalAdvanceWindow。
const amazonNaturalAdvanceWindow = 1500 * time.Millisecond

// amazonSeekStartTimeout 同 Swift 侧 seekStartTimeout。
const amazonSeekStartTimeout = 2 * time.Second

func applyAmazonEvent(s amazonPlayheadState, ev amazonEvent, t time.Time) amazonPlayheadState {
	switch ev.kind {
	case amazonTrackStarted:
		s.trackID, s.trackStartedAt = ev.trackID, t
		s.base, s.paused, s.pristine = 0, false, true
		s.awaitingStartUntil = time.Time{}
		s.startedNaturally = !s.lastEndOfStreamAt.IsZero() && t.Sub(s.lastEndOfStreamAt) <= amazonNaturalAdvanceWindow
		s.lastEndOfStreamAt, s.lastFlushAt = time.Time{}, time.Time{}
		s.since = time.Time{}
		if !s.stalled {
			s.since = t
		}
	case amazonPaused:
		s.base = amazonPosition(s, t)
		s.paused, s.pristine = true, false
		s.since, s.awaitingStartUntil = time.Time{}, time.Time{}
	case amazonResumed:
		s.paused = false
		if !s.stalled {
			s.since = t
		}
	case amazonSeek:
		s.base = max(0, ev.seekTo)
		s.since, s.awaitingStartUntil = time.Time{}, time.Time{}
		if !s.paused && !s.stalled {
			s.awaitingStartUntil = t.Add(amazonSeekStartTimeout)
		}
		// 开播时也会先定位到 0,那一下不算拖动。拖动会清空输出缓冲,自动连播的提前量随之作废。
		if ev.seekTo > 0 {
			s.pristine, s.startedNaturally = false, false
			s.lastFlushAt = t
		}
	case amazonEndOfStream:
		s.lastEndOfStreamAt = t
	case amazonStarting:
		if !s.awaitingStartUntil.IsZero() {
			s.awaitingStartUntil = time.Time{}
			if !s.paused && !s.stalled {
				s.since = t
			}
		}
	case amazonStallOn:
		s.base = amazonPosition(s, t)
		s.stalled = true
		s.since, s.awaitingStartUntil = time.Time{}, time.Time{}
	case amazonStallOff:
		s.stalled = false
		if !s.paused {
			s.since = t
		}
	}
	return s
}

func amazonPosition(s amazonPlayheadState, t time.Time) float64 {
	if !s.awaitingStartUntil.IsZero() {
		return s.base + max(0, t.Sub(s.awaitingStartUntil).Seconds())
	}
	if s.since.IsZero() {
		return s.base
	}
	return s.base + max(0, t.Sub(s.since).Seconds())
}

// calibrateAmazonPlayhead:系统时间戳就是这首真正出声的时刻,精确到微秒。开播之后没暂停、没拖动过,
// 而且跟日志的开播差得不多(同一次开播)时,把起点换成它。
func calibrateAmazonPlayhead(s amazonPlayheadState, metadataTS time.Time) amazonPlayheadState {
	if !s.pristine || s.base != 0 || s.since.IsZero() || s.trackStartedAt.IsZero() || metadataTS.IsZero() {
		return s
	}
	lead := metadataTS.Sub(s.trackStartedAt)
	if lead < -amazonCalibrationWindow || lead > amazonCalibrationWindow {
		return s
	}
	s.since = metadataTS
	return s
}

// amazonMetadataStale:元数据时间戳比日志里这首的开播早出这么多,是上一次会话留下的旧曲目。
func amazonMetadataStale(s amazonPlayheadState, metadataTS time.Time) bool {
	if s.trackStartedAt.IsZero() || metadataTS.IsZero() {
		return false
	}
	return metadataTS.Before(s.trackStartedAt.Add(-amazonStaleMetadataLead))
}

// amazonLogCovers:日志里这首的开播对得上系统元数据(同一首歌)。
func amazonLogCovers(s amazonPlayheadState, metadataTS time.Time) bool {
	if s.trackStartedAt.IsZero() || metadataTS.IsZero() {
		return false
	}
	lead := metadataTS.Sub(s.trackStartedAt)
	return lead >= -amazonStaleMetadataLead && lead <= amazonCalibrationWindow
}

// amazonSelfTimer 同 Swift 侧 AmazonMusicPlayhead.SelfTimer。
type amazonSelfTimer struct {
	trackKey string
	base     float64
	since    time.Time
}

func (t amazonSelfTimer) position(at time.Time) float64 {
	if t.since.IsZero() {
		return t.base
	}
	return t.base + max(0, at.Sub(t.since).Seconds())
}

func advanceAmazonSelfTimer(prev amazonSelfTimer, key string, metadataTS time.Time, playing bool, observedAt time.Time) amazonSelfTimer {
	if prev.trackKey != key {
		start := observedAt
		if !metadataTS.IsZero() && metadataTS.Before(observedAt) {
			start = metadataTS
		}
		if playing {
			return amazonSelfTimer{trackKey: key, since: start}
		}
		return amazonSelfTimer{trackKey: key, base: max(0, observedAt.Sub(start).Seconds())}
	}
	next := prev
	switch {
	case !prev.since.IsZero() && !playing:
		next.base, next.since = prev.position(observedAt), time.Time{}
	case prev.since.IsZero() && playing:
		next.since = observedAt
	}
	return next
}

type amazonReading struct {
	position      float64
	fromLog       bool
	staleMetadata bool
}

// amazonReadingFor 在两层之间选:日志对得上这首就用日志,否则用自记时。hasLog=false 表示日志读不到。
func amazonReadingFor(logState amazonPlayheadState, hasLog bool, timer amazonSelfTimer, metadataTS, now time.Time) amazonReading {
	if hasLog {
		if amazonMetadataStale(logState, metadataTS) {
			return amazonReading{staleMetadata: true, fromLog: true}
		}
		if amazonLogCovers(logState, metadataTS) {
			pos := amazonPosition(calibrateAmazonPlayhead(logState, metadataTS), now) - amazonLeadFor(logState)
			return amazonReading{position: max(0, pos), fromLog: true}
		}
	}
	return amazonReading{position: timer.position(now)}
}

// ---- 自动连播的提前量(App 校准,见文件头注)----

// amazonLeadRecord 同 Swift 侧 AmazonMusicLeadRecord,json tag 逐字节一致。
type amazonLeadRecord struct {
	TrackID     string  `json:"track_id"`
	StartedAtMs int64   `json:"started_at_ms"`
	LeadSecs    float64 `json:"lead_secs"`
	WrittenAtMs int64   `json:"written_at_ms"`
}

// amazonLeadStartTolerance:App 记的开播时刻(实时读到的那一刻)与这边(行首秒 + 0.5)最多差多少还算同一次开播。
const amazonLeadStartTolerance = 2 * time.Second

var amazonLeadPath string

func setAmazonLeadPath(path string) { amazonLeadPath = path }

// amazonLeadFor:这首 App 按界面校准出的提前量(自动连播开的头,或卡顿后重新校准的);对不上返回 0。
func amazonLeadFor(s amazonPlayheadState) float64 {
	if amazonLeadPath == "" {
		return 0
	}
	data, err := os.ReadFile(amazonLeadPath)
	if err != nil {
		return 0
	}
	var rec amazonLeadRecord
	if json.Unmarshal(data, &rec) != nil {
		return 0
	}
	return amazonLeadApplies(rec, s)
}

// amazonLeadApplies 纯函数:记录是不是这一首这一次开播的。
func amazonLeadApplies(rec amazonLeadRecord, s amazonPlayheadState) float64 {
	if rec.TrackID != s.trackID || rec.LeadSecs == 0 || rec.LeadSecs < -4 || rec.LeadSecs > 8 {
		return 0
	}
	d := time.UnixMilli(rec.StartedAtMs).Sub(s.trackStartedAt)
	if d < -amazonLeadStartTolerance || d > amazonLeadStartTolerance {
		return 0
	}
	// 行首只到秒(取中点),拖动的时刻最多比实际晚半秒;留一秒余量,别把那之后刚写的那份当成旧的。
	if !s.lastFlushAt.IsZero() && time.UnixMilli(rec.WrittenAtMs).Before(s.lastFlushAt.Add(-time.Second)) {
		return 0
	}
	return rec.LeadSecs
}

// ---- 日志读取与快照改写 ----

// amazonMusicLogOverride:单测指向临时文件。
var amazonMusicLogOverride string

func amazonMusicLogPath() string {
	if amazonMusicLogOverride != "" {
		return amazonMusicLogOverride
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "Amazon Music", "Logs", "AmazonMusic.log")
}

// amazonLogTail 增量读日志:记着读到哪了,每拍只读新写的部分;文件变短(Amazon Music 重启后重写)就从头读。
// 第一次读只看末尾 amazonLogTailOnFirstRead 字节(一份日志一天能长到几 MB),但至少从最后一次开播读起,见 amazonReplayStart。
type amazonLogTail struct {
	path    string
	offset  int64
	partial []byte
	state   amazonPlayheadState
	queue   []string // 最近一次 updateQueue 的窗口(当前这首 + 后两首),见 parseAmazonQueueLine
	seen    bool     // 认出过至少一个事件
	ok      bool     // 这一拍读到了文件
	// cloudQueue:最近一次开播走的是云端队列(电台,见 amazonPlaybackStartKind)。后面放什么由服务器边放边定,
	// 同专辑的其它歌基本放不到。
	cloudQueue bool
}

// amazonPlaybackStartKind 认日志里的开播请求:云端队列(`CQPlaybackRequestImpl … StartingCQPlayback`,电台一类:
// 开播带一串种子 ASIN,之后剩 5 首就向服务器再要一批)返回 cloud=true;普通开播(`BasePlaybackRequest …
// StartPlaybackLookupCompleted`,歌单 / 专辑)和播客开播返回 cloud=false。不是开播行 ok=false。纯函数。
func amazonPlaybackStartKind(line string) (cloud, ok bool) {
	switch {
	case strings.Contains(line, "StartingCQPlayback"):
		return true, true
	case strings.Contains(line, "StartPlaybackLookupCompleted"), strings.Contains(line, "StartPodcastPlayback"):
		return false, true
	}
	return false, false
}

// amazonReplayStart:第一次读从哪个字节开始 —— 末尾 tail 字节,但不晚于最后一次开播那一行(再往前留 amazonReplayLead,
// 带上紧挨着它的 End of stream,自然连播要靠它认)。开播后暂停得久,Amazon 照样往日志里写,开播行会被推到末尾那段之前:
// 实测暂停 54 分钟后开播行离末尾 303 KB,只看末尾就认不出这首、退回自记时,位置按开播时刻一路外推到曲尾卡住。
// 同 Swift 侧 AmazonMusicPlayhead.replayStart。纯函数。
func amazonReplayStart(data []byte, tail int) int {
	start := max(0, len(data)-tail)
	last := bytes.LastIndex(data, []byte("new track playing"))
	if last < 0 || last >= start {
		return start
	}
	back := max(0, last-amazonReplayLead)
	if nl := bytes.LastIndexByte(data[:back], '\n'); nl >= 0 {
		return nl + 1
	}
	return 0
}

// amazonReplayLead 同 Swift 侧 replayLead。
const amazonReplayLead = 4096

// amazonLastStartIsCloudQueue:一段日志里最后一次开播是不是云端队列。第一次只读末尾一段,开播行常在更前面,
// 这里补看一遍前面那部分。
func amazonLastStartIsCloudQueue(data []byte) bool {
	cq := bytes.LastIndex(data, []byte("StartingCQPlayback"))
	plain := max(bytes.LastIndex(data, []byte("StartPlaybackLookupCompleted")), bytes.LastIndex(data, []byte("StartPodcastPlayback")))
	return cq > plain
}

func (t *amazonLogTail) poll() {
	f, err := os.Open(t.path)
	if err != nil {
		t.ok = false
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.ok = false
		return
	}
	t.ok = true
	size := info.Size()
	if size < t.offset {
		t.offset, t.partial, t.state, t.queue, t.seen, t.cloudQueue = 0, nil, amazonPlayheadState{}, nil, false, false
	}
	if t.offset == 0 && size > amazonLogTailOnFirstRead {
		if all, err := io.ReadAll(io.LimitReader(f, size)); err == nil {
			t.offset = int64(amazonReplayStart(all, amazonLogTailOnFirstRead))
			t.cloudQueue = amazonLastStartIsCloudQueue(all[:t.offset])
		} else {
			t.offset = size - amazonLogTailOnFirstRead
		}
	}
	if size == t.offset {
		return
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return
	}
	chunk, err := io.ReadAll(io.LimitReader(f, size-t.offset))
	if err != nil {
		return
	}
	t.offset += int64(len(chunk))
	data := append(t.partial, chunk...)
	last := bytes.LastIndexByte(data, '\n')
	if last < 0 {
		t.partial = data
		return
	}
	t.partial = append([]byte(nil), data[last+1:]...)
	for _, raw := range bytes.Split(data[:last], []byte{'\n'}) {
		if q, ok := parseAmazonQueueLine(string(raw)); ok {
			t.queue = q
			continue
		}
		if cloud, ok := amazonPlaybackStartKind(string(raw)); ok {
			t.cloudQueue = cloud
			continue
		}
		at, ev, ok := parseAmazonLogLine(string(raw))
		if !ok {
			continue
		}
		t.state = applyAmazonEvent(t.state, ev, at.Add(amazonReplayedLineOffset))
		t.seen = true
	}
}

var (
	amazonClockMu      sync.Mutex
	amazonClockTail    *amazonLogTail
	amazonClockTimer   amazonSelfTimer
	amazonClockLastSrc string
	// amazonCurrentTrack:最近一拍用日志算位置的那首(系统报的歌手 / 歌名 + 日志里的曲目标识),给曲目页用。
	amazonCurrentTrack struct{ artist, title, trackID string }
)

// amazonTrackURL:日志里的 `asin://<ASIN>` 换成公开曲目页。ASIN 是 10 位大写字母数字,别的形状(播客)不给。
func amazonTrackURL(trackID string) string {
	asin, ok := strings.CutPrefix(trackID, "asin://")
	if !ok || len(asin) != 10 {
		return ""
	}
	for _, c := range asin {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return ""
		}
	}
	return "https://music.amazon.com/tracks/" + asin
}

// amazonTrackURLFor:正用 Amazon Music 放、而且时钟那一拍是用日志对上这首的,返回它的曲目页。
func amazonTrackURLFor(bundleID, artist, title string) string {
	if bundleID != amazonMusicBundleID {
		return ""
	}
	amazonClockMu.Lock()
	defer amazonClockMu.Unlock()
	if amazonCurrentTrack.artist != artist || amazonCurrentTrack.title != title {
		return ""
	}
	return amazonTrackURL(amazonCurrentTrack.trackID)
}

// amazonAdvanceClockLocked 读进日志的新行。起点换成系统时间戳这一步先记进状态(同 Swift 侧 AmazonMusicLogWatcher.reading):
// 这一轮读到的暂停 / 卡顿按状态里的起点记停表位置,两边起点不同暂停那一下就跳一截。新开播的那首窗口对不上,这一步什么都不改。
// 调用方持有 amazonClockMu。
func amazonAdvanceClockLocked(metadataTS time.Time) {
	if amazonClockTail == nil || amazonClockTail.path != amazonMusicLogPath() {
		amazonClockTail = &amazonLogTail{path: amazonMusicLogPath()}
	}
	if amazonClockTail.seen && amazonLogCovers(amazonClockTail.state, metadataTS) {
		amazonClockTail.state = calibrateAmazonPlayhead(amazonClockTail.state, metadataTS)
	}
	amazonClockTail.poll()
}

// amazonStateIsStale:这份 media-control 状态是不是 Amazon Music 上一次会话留下的旧曲目(applyAmazonMusicClock 返回 false 的那种)。
// poller 把它当读空:连着几拍都是它就按停播清掉当前曲目,不无限期按住上一首(别家那首会一直显示在放、反复报 now-playing)。
// App 侧同一判据当「不是歌」、什么都不显示(MediaControlClient 里 staleMetadata 那一支)。
func amazonStateIsStale(state map[string]any) bool {
	s := extract(state)
	if s.Bundle != amazonMusicBundleID || s.Title == "" {
		return false
	}
	amazonClockMu.Lock()
	defer amazonClockMu.Unlock()
	amazonAdvanceClockLocked(s.MetadataTS)
	return amazonClockTail.ok && amazonClockTail.seen && amazonMetadataStale(amazonClockTail.state, s.MetadataTS)
}

// applyAmazonMusicClock 把 Amazon Music 快照的位置 / 锚点换成自己算的(见文件头注)。返回 false = 这一拍是
// 上一次会话留下的旧曲目,调用方不采纳。跟 applyRadioClock 同一个位置调用:下游拿到的是一份正常的快照。
func applyAmazonMusicClock(s *snapshot, now time.Time) bool {
	if s == nil || s.Bundle != amazonMusicBundleID || s.Title == "" {
		return true
	}
	amazonClockMu.Lock()
	defer amazonClockMu.Unlock()
	metadataTS := s.MetadataTS
	amazonAdvanceClockLocked(metadataTS)
	amazonClockTimer = advanceAmazonSelfTimer(amazonClockTimer, s.key(), metadataTS, s.Playing, now)
	r := amazonReadingFor(amazonClockTail.state, amazonClockTail.ok && amazonClockTail.seen, amazonClockTimer, metadataTS, now)
	if r.staleMetadata {
		return false
	}
	src := "self-timer"
	amazonCurrentTrack.artist, amazonCurrentTrack.title, amazonCurrentTrack.trackID = "", "", ""
	if r.fromLog {
		src = "log"
		amazonCurrentTrack.artist, amazonCurrentTrack.title = s.Artist, s.Title
		amazonCurrentTrack.trackID = amazonClockTail.state.trackID
	}
	if src != amazonClockLastSrc {
		st := amazonClockTail.state
		log.Printf("amazon music clock: position from %s (key=%q metadata_ts=%s log_track_started=%s lead=%.1fs log_ok=%v log_events=%v)",
			src, s.key(), logClockMillis(metadataTS), logClockMillis(st.trackStartedAt),
			metadataTS.Sub(st.trackStartedAt).Seconds(), amazonClockTail.ok, amazonClockTail.seen)
		amazonClockLastSrc = src
	}
	s.Elapsed, s.AnchorElapsed, s.McTS = r.position, r.position, now
	return true
}
