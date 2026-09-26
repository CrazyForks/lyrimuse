package main

import (
	"reflect"
	"testing"
)

// KKBOX 开播先发一帧只有歌名的(歌手空、时长 0),约半秒后补齐:那一帧不采纳,补齐之后照常;专辑名不作要求。
func TestKKBOXArtistArrivesLate(t *testing.T) {
	if !trustedPlaybackNotASong(kkboxBundleID, "", "") {
		t.Error("开播那一帧还没有歌手,不该采纳")
	}
	if trustedPlaybackNotASong(kkboxBundleID, "Taylor Swift (泰勒絲)", "") {
		t.Error("歌手补齐之后照常采纳,没有专辑名也认")
	}
	if trustedPlaybackNotASong(kugouMusicBundleID, "", "") {
		t.Error("只管 artistArrivesLate 的播放器,别的内置播放器不受影响")
	}
	raw := map[string]any{"title": "Opalite", "artist": "", "duration": 0.0, "playing": false}
	if !builtinArtistNotReady(kkboxBundleID, raw) {
		t.Error("开播那一帧(歌手空、时长 0、没在放):还没准备好")
	}
	podcast := map[string]any{"title": "1989 - Deluxe - KKBOX", "artist": "", "album": "", "duration": 2143.19, "playing": true}
	if builtinArtistNotReady(kkboxBundleID, podcast) || !builtinArtistlessContent(kkboxBundleID, podcast) {
		t.Error("播客单集(歌手空、有时长、在放):不是开播那一帧,是非歌曲内容")
	}
	if builtinArtistlessContent(kkboxBundleID, raw) {
		t.Error("开播那一帧不是非歌曲内容")
	}
	if builtinArtistlessContent(kugouMusicBundleID, podcast) {
		t.Error("只管 artistArrivesLate 的播放器(决策 41)")
	}
	paused := map[string]any{"title": "1989 - Deluxe - KKBOX", "artist": "", "duration": 2143.19, "playing": false}
	if !builtinArtistNotReady(kkboxBundleID, paused) {
		t.Error("歌手空又没在放:分不出是不是开播那一帧,照旧当还没准备好")
	}
}

// KKBOX 歌手空的(播客单集)不拿去搜歌词。
func TestKKBOXArtistlessNotEnriched(t *testing.T) {
	if got := trackEnrichment("", "1989 - Deluxe - KKBOX", "", kkboxBundleID, 2143, true, false); got != nil {
		t.Errorf("KKBOX 歌手空的内容不该解析: %v", got)
	}
}

// 信任列表里的 KKBOX 升级后补进选中集合;勾着自动识别的不用补。
func TestPromoteTrustedBuiltins(t *testing.T) {
	trusted := map[string]string{kkboxBundleID: "KKBOX", "com.apple.Safari": "Safari"}
	got := promoteTrustedBuiltins(map[string]bool{playerQQMusic: true}, trusted)
	if want := map[string]bool{playerQQMusic: true, playerKKBOX: true}; !reflect.DeepEqual(got, want) {
		t.Errorf("没勾自动识别: got %v want %v", got, want)
	}
	got = promoteTrustedBuiltins(map[string]bool{playerAuto: true}, trusted)
	if want := map[string]bool{playerAuto: true}; !reflect.DeepEqual(got, want) {
		t.Errorf("勾着自动识别: got %v want %v", got, want)
	}
	got = promoteTrustedBuiltins(map[string]bool{playerSpotify: true}, map[string]string{"com.apple.Safari": "Safari"})
	if want := map[string]bool{playerSpotify: true}; !reflect.DeepEqual(got, want) {
		t.Errorf("没有内置播放器: got %v want %v", got, want)
	}
	if tp := resolveTrustedPlayers(trusted); tp[kkboxBundleID] != "" || tp["com.apple.Safari"] != "Safari" {
		t.Errorf("KKBOX 内置之后剔出信任列表: %v", tp)
	}
}
