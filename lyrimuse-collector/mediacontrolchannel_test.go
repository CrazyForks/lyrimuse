package main

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func resetMediaControlChannelForTest(t *testing.T) {
	t.Helper()
	oldTest, oldQuery := mediaControlChannelTest, channelFallbackQuery
	reset := func() {
		mediaControlChannelMu.Lock()
		mediaControlExecFails, mediaControlNullStreak = 0, 0
		mediaControlTestFailed, mediaControlTestRunning, mediaControlFallbackIn, mediaControlSeenState = false, false, false, false
		mediaControlLastTest = time.Time{}
		mediaControlChannelMu.Unlock()
	}
	reset()
	t.Cleanup(func() {
		mediaControlChannelTest, channelFallbackQuery = oldTest, oldQuery
		reset()
	})
}

func waitMediaControlTestDone(t *testing.T) {
	t.Helper()
	for i := 0; i < 200; i++ {
		mediaControlChannelMu.Lock()
		running := mediaControlTestRunning
		mediaControlChannelMu.Unlock()
		if !running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("自检没跑完")
}

func TestChannelFallbackCandidates(t *testing.T) {
	if got := channelFallbackCandidates(nil); !reflect.DeepEqual(got, []string{appleMusicBundleID, spotifyBundleID}) {
		t.Errorf("自动识别 = %v", got)
	}
	if got := channelFallbackCandidates(map[string]bool{spotifyBundleID: true, "com.tencent.QQMusicMac": true}); !reflect.DeepEqual(got, []string{spotifyBundleID}) {
		t.Errorf("多选 = %v", got)
	}
	if got := channelFallbackCandidates(map[string]bool{"com.tencent.QQMusicMac": true}); len(got) != 0 {
		t.Errorf("只勾了没有 AppleScript 的播放器 = %v", got)
	}
}

func TestMediaControlChannelBrokenByExecFailures(t *testing.T) {
	resetMediaControlChannelForTest(t)
	mediaControlChannelTest = func(context.Context) error { return nil }
	for i := 0; i < mediaControlExecFailThreshold-1; i++ {
		noteMediaControlExec(false, false)
	}
	waitMediaControlTestDone(t)
	if mediaControlChannelBroken() {
		t.Fatal("阈值之前不算坏")
	}
	noteMediaControlExec(false, false)
	if !mediaControlChannelBroken() {
		t.Fatal("连续报错到阈值应算坏")
	}
	noteMediaControlExec(true, false)
	if mediaControlChannelBroken() {
		t.Fatal("读到一份快照就恢复")
	}
}

func TestMediaControlChannelBrokenByTestAndRetestInterval(t *testing.T) {
	resetMediaControlChannelForTest(t)
	calls := 0
	mediaControlChannelTest = func(context.Context) error { calls++; return errors.New("exit status 4") }
	maybeTestMediaControlChannel(true)
	waitMediaControlTestDone(t)
	if !mediaControlChannelBroken() {
		t.Fatal("自检失败应算坏")
	}
	// 通道坏了时 media-control 照常退出、回 null;攒满一轮也不会在间隔内重复自检。
	for i := 0; i < mediaControlNullRetestStreak; i++ {
		noteMediaControlExec(true, true)
	}
	waitMediaControlTestDone(t)
	if calls != 1 {
		t.Fatalf("间隔内不该复测: calls=%d", calls)
	}
	if !mediaControlChannelBroken() {
		t.Fatal("回 null 不能把自检的结论清掉")
	}
}

func TestStateWhileChannelBrokenPrefersPlaying(t *testing.T) {
	resetMediaControlChannelForTest(t)
	asked := []string{}
	channelFallbackQuery = func(_ context.Context, b string) map[string]any {
		asked = append(asked, b)
		switch b {
		case appleMusicBundleID:
			return map[string]any{"title": "A", "playing": false}
		case spotifyBundleID:
			return map[string]any{"title": "S", "playing": true}
		}
		return nil
	}
	if _, ok := stateWhileChannelBroken(context.Background(), nil); ok || len(asked) != 0 {
		t.Fatalf("通道没坏时不问: ok=%v asked=%v", ok, asked)
	}
	mediaControlChannelMu.Lock()
	mediaControlTestFailed = true
	mediaControlChannelMu.Unlock()
	state, ok := stateWhileChannelBroken(context.Background(), nil)
	if !ok || state["title"] != "S" {
		t.Fatalf("在放的优先: ok=%v state=%v", ok, state)
	}
	state, ok = stateWhileChannelBroken(context.Background(), map[string]bool{appleMusicBundleID: true})
	if !ok || state["title"] != "A" {
		t.Fatalf("多选只问勾了的,暂停的也要: ok=%v state=%v", ok, state)
	}
}

// getAutoDetectedState:media-control 回 null 且通道坏了时,直问得到的快照原样交出去。
func TestAutoDetectedFallsBackWhenChannelBroken(t *testing.T) {
	resetMediaControlChannelForTest(t)
	old := fetchRawNowPlaying
	fetchRawNowPlaying = func(context.Context) (map[string]any, string, bool) { return map[string]any{}, "", true }
	t.Cleanup(func() { fetchRawNowPlaying = old })
	channelFallbackQuery = func(_ context.Context, b string) map[string]any {
		if b == appleMusicBundleID {
			return map[string]any{"title": "A", "playing": true}
		}
		return nil
	}
	if state, ok := getAutoDetectedState(context.Background()); !ok || len(state) != 0 {
		t.Fatalf("通道没坏时照旧当没在放: ok=%v state=%v", ok, state)
	}
	mediaControlChannelMu.Lock()
	mediaControlTestFailed = true
	mediaControlChannelMu.Unlock()
	if state, ok := getAutoDetectedState(context.Background()); !ok || state["title"] != "A" {
		t.Fatalf("通道坏了应直问: ok=%v state=%v", ok, state)
	}
}

// 读到过真快照之后,自检偶发失败不把读取切到直问,也不再复测。
func TestMediaControlChannelIgnoresTestAfterRealSnapshot(t *testing.T) {
	resetMediaControlChannelForTest(t)
	calls := 0
	mediaControlChannelTest = func(context.Context) error { calls++; return errors.New("setup_done timeout") }
	noteMediaControlExec(true, false)
	maybeTestMediaControlChannel(true)
	waitMediaControlTestDone(t)
	if mediaControlChannelBroken() {
		t.Fatal("读到过真快照,自检失败不该算坏")
	}
	for i := 0; i < mediaControlNullRetestStreak; i++ {
		noteMediaControlExec(true, true)
	}
	waitMediaControlTestDone(t)
	if calls != 1 {
		t.Fatalf("读到过真快照之后不该再因 null 复测: calls=%d", calls)
	}
	for i := 0; i < mediaControlExecFailThreshold; i++ {
		noteMediaControlExec(false, false)
	}
	if !mediaControlChannelBroken() {
		t.Fatal("连续子进程报错照样算坏")
	}
}
