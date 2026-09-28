package main

import (
	"context"
	"log"
	"os/exec"
	"sync"
	"time"
)

// media-control 通道坏了时,直接问 Apple Music / Spotify。
//
// media-control 走的是私有 MediaRemote 通道,系统更新可能让它失效:要么子进程直接报错,要么照常退出、却对谁都
// 回 null。「自动识别」和多选的基座都是它,坏了之后连有 AppleScript 字典的 Apple Music / Spotify 也认不出来。
// focusfallback.go 那条回退只在「上一份被接受的快照」存在时才走,而通道一开机就坏的话这个开关永远点不亮。
//
// 判定通道坏了(两条任一):
//   - 连续 mediaControlExecFailThreshold 次子进程报错;
//   - `media-control test` 失败。它在常驻实例启动后跑一次,之后连续 mediaControlNullRetestStreak 拍都回 null、
//     或子进程报错时复测,两次复测至少隔 mediaControlRetestInterval。本进程读到过一份真快照之后不再复测、
//     也不再认它的失败:那已经证明通道能用,`test` 自己偶发失败时不该把读取切到直问。
//
// 坏了之后按顺序问还开着的 Apple Music、Spotify(JXA 脚本自带 running 守卫,不会把播放器拉起来),在放的优先,
// 都没在放取第一个暂停着的。多选时只问用户勾了的。QQ 音乐 / 网易云 / 酷狗 / 汽水 / KKBOX / Amazon Music 没有 AppleScript
// 字典,按 bundle id 直查系统的那条路(nowplaying-clients)也是 MediaRemote,跟着一起坏,这里不问。
// App 侧同一件事在 MediaControlClient.snapshotWhileChannelBroken,两侧口径一致。

const (
	mediaControlExecFailThreshold = 3
	mediaControlNullRetestStreak  = 30
	mediaControlRetestInterval    = 10 * time.Minute
	mediaControlTestTimeout       = 10 * time.Second
)

var (
	mediaControlChannelMu   sync.Mutex
	mediaControlExecFails   int
	mediaControlNullStreak  int
	mediaControlTestFailed  bool
	mediaControlTestRunning bool
	mediaControlLastTest    time.Time
	mediaControlFallbackIn  bool // 此刻正靠直问取数(只为日志只在翻转时打一条)
	mediaControlSeenState   bool // 本进程读到过一份真快照
	mediaControlChannelNow  = time.Now
)

// mediaControlChannelTest 跑一次 `media-control test`。单测替换它。
var mediaControlChannelTest = func(ctx context.Context) error {
	bin := mediaControlBinaryPath()
	if bin == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, mediaControlTestTimeout)
	defer cancel()
	return exec.CommandContext(ctx, bin, "test").Run()
}

// channelFallbackQuery 问一个播放器自己;没在跑 / 停止 / 问不到返回 nil。单测替换它。
var channelFallbackQuery = func(ctx context.Context, bundleID string) map[string]any {
	return focusFallbackAppleScript(ctx, bundleID)
}

// noteMediaControlExec 记一次 media-control 读取的结果:execOK=false 是子进程报错,null 是没有任何 App 在报告。
func noteMediaControlExec(execOK, null bool) {
	mediaControlChannelMu.Lock()
	retest := false
	switch {
	case !execOK:
		mediaControlExecFails++
		retest = true
	case null:
		mediaControlExecFails = 0
		mediaControlNullStreak++
		retest = mediaControlNullStreak%mediaControlNullRetestStreak == 0
	default:
		mediaControlExecFails, mediaControlNullStreak = 0, 0
		// 真读到了一份快照:通道是好的,不必等下一次自检。
		mediaControlTestFailed, mediaControlSeenState = false, true
		if mediaControlFallbackIn {
			mediaControlFallbackIn = false
			log.Printf("media-control channel usable again; back on media-control")
		}
	}
	retest = retest && !mediaControlSeenState
	mediaControlChannelMu.Unlock()
	if retest {
		maybeTestMediaControlChannel(false)
	}
}

// maybeTestMediaControlChannel 在后台跑一次自检。force=false 时两次之间至少隔 mediaControlRetestInterval。
func maybeTestMediaControlChannel(force bool) {
	mediaControlChannelMu.Lock()
	now := mediaControlChannelNow()
	if mediaControlTestRunning || (!force && !mediaControlLastTest.IsZero() && now.Sub(mediaControlLastTest) < mediaControlRetestInterval) {
		mediaControlChannelMu.Unlock()
		return
	}
	mediaControlTestRunning, mediaControlLastTest = true, now
	mediaControlChannelMu.Unlock()
	go func() {
		err := mediaControlChannelTest(context.Background())
		mediaControlChannelMu.Lock()
		was := mediaControlTestFailed
		mediaControlTestFailed = err != nil && !mediaControlSeenState
		mediaControlTestRunning = false
		mediaControlChannelMu.Unlock()
		switch {
		case err != nil && !was:
			warnf("media-control channel test failed (%v); asking Apple Music / Spotify directly until it recovers", err)
		case err == nil && was:
			log.Printf("media-control channel test passed again")
		}
	}()
}

// mediaControlChannelBroken:此刻是否按通道坏了处理。
func mediaControlChannelBroken() bool {
	mediaControlChannelMu.Lock()
	defer mediaControlChannelMu.Unlock()
	return mediaControlExecFails >= mediaControlExecFailThreshold || mediaControlTestFailed
}

// channelFallbackCandidates:通道坏了时按顺序问谁。selected 为 nil 是「自动识别」,两家都问;
// 否则只问勾了的。纯函数。
func channelFallbackCandidates(selected map[string]bool) []string {
	var out []string
	for _, b := range []string{appleMusicBundleID, spotifyBundleID} {
		if selected == nil || selected[b] {
			out = append(out, b)
		}
	}
	return out
}

// stateWhileChannelBroken:通道坏了时直接问 Apple Music / Spotify。ok=false = 通道没坏 / 谁都没问到。
func stateWhileChannelBroken(ctx context.Context, selected map[string]bool) (map[string]any, bool) {
	if !mediaControlChannelBroken() {
		mediaControlChannelMu.Lock()
		was := mediaControlFallbackIn
		mediaControlFallbackIn = false
		mediaControlChannelMu.Unlock()
		if was {
			log.Printf("media-control channel usable again; back on media-control")
		}
		return nil, false
	}
	var paused map[string]any
	var pausedFrom, from string
	var state map[string]any
	for _, b := range channelFallbackCandidates(selected) {
		s := channelFallbackQuery(ctx, b)
		if len(s) == 0 {
			continue
		}
		if playing, _ := s["playing"].(bool); playing {
			state, from = s, b
			break
		}
		if paused == nil {
			paused, pausedFrom = s, b
		}
	}
	if state == nil {
		state, from = paused, pausedFrom
	}
	mediaControlChannelMu.Lock()
	first := state != nil && !mediaControlFallbackIn
	mediaControlFallbackIn = state != nil
	mediaControlChannelMu.Unlock()
	if state == nil {
		return nil, false
	}
	if first {
		log.Printf("media-control channel broken; reading %s via AppleScript", from)
	}
	return state, true
}
