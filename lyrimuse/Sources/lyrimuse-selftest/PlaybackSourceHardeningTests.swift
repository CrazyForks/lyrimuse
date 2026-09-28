import Foundation
import LyrimuseCore

// 播放源(LyrimuseCore/Local)审计之后的加固:轮询单飞、停播清理、焦点宽限的归类、最近记录的计次与拼页、
// 广告探针 / 跳过复核、资料库删除、缓存读取与退避。

@MainActor
func runPlaybackSourceHardeningTests() {
    pollSingleFlightTests()
    playbackStateTests()
    recentListensTests()
    browserProbeTests()
    libraryAndCacheTests()
    replayNoDurationFirstTick()
}

// ---- 轮询单飞 ----

@MainActor
private func pollSingleFlightTests() {
    var f = PollSingleFlight()
    expectEqual(f.begin(), true, "轮询单飞: 空闲时可以起一轮")
    expectEqual(f.begin(), false, "轮询单飞: 在飞时不再起第二轮")
    expectEqual(f.begin(), false, "轮询单飞: 在飞时再来多少次也只合并成一次补跑")
    expectEqual(f.finish(), true, "轮询单飞: 在飞期间有人要过 → 收尾时补跑一轮")
    expectEqual(f.inFlight, false, "轮询单飞: 收尾后放开")
    expectEqual(f.begin(), true, "轮询单飞: 补跑那一轮能起")
    expectEqual(f.finish(), false, "轮询单飞: 期间没人要过 → 不补跑")
    f.invalidateInFlight()
    expectEqual(f.rerunRequested, false, "轮询单飞: 没有在飞的轮次时拖动不记补跑")
    _ = f.begin()
    f.invalidateInFlight()
    expectEqual(f.finish(), true, "轮询单飞: 拖动作废了在飞那一轮 → 回来后补跑一轮拿拖动之后的状态")
}

// ---- 停播 / 焦点宽限 / 缺时长 / 恢复信号 ----

@MainActor
private func playbackStateTests() {
    typealias P = LocalPlaybackSource
    expectEqual(P.hasTrackStateToClear(isPlaying: false, title: "暂停中的歌", lastKey: "k", pausedPositionMs: 12_000, hasAnchor: false), true,
                "停播清理: 先暂停再退出播放器 —— 没在播,但还挂着这首歌,要清")
    expectEqual(P.hasTrackStateToClear(isPlaying: true, title: "", lastKey: "", pausedPositionMs: nil, hasAnchor: false), true,
                "停播清理: 在播时照旧清")
    expectEqual(P.hasTrackStateToClear(isPlaying: false, title: "", lastKey: "", pausedPositionMs: nil, hasAnchor: false), false,
                "停播清理: 清过一次之后什么都不剩,不再重复清")

    typealias M = MediaControlClient
    expectEqual(M.isFocusHeldElsewhere(.targetNotPlayingMusic), false,
                "焦点宽限: 选中的播放器自己在放播客,不是焦点被占,不给 300 秒宽限")
    expectEqual(M.nilSnapshotClearsState(consecutiveNilCount: 2, failure: .targetNotPlayingMusic, nilStreakSeconds: 4), true,
                "焦点宽限: 播放器在放非音乐 → 按短宽限清(跟 collector 约 3 拍清一致)")
    expectEqual(M.isFocusHeldElsewhere(.notASong), true, "焦点宽限: 别的 App(浏览器)在放非歌曲内容仍算焦点被占")
    expectEqual(M.failureWithoutFallbackTarget(targetConfirmedGone: true), .appleScriptUnavailable,
                "焦点宽限: 回退已确认目标播放器不在 → 之后几拍记成问不到,不停在焦点被占那一档")
    expectEqual(M.failureWithoutFallbackTarget(targetConfirmedGone: false), nil,
                "焦点宽限: 从没有过回退目标时不改失败原因")

    expectEqual(P.lyricsLookupDuration(isRadio: false, isMusicVideo: true, duration: 245), nil,
                "查歌词时长: MV 当未知(collector 只用基条目,不建时长变体)")
    expectEqual(P.lyricsLookupDuration(isRadio: true, isMusicVideo: false, duration: 3390), nil, "查歌词时长: 电台当未知")
    expectEqual(P.lyricsLookupDuration(isRadio: false, isMusicVideo: false, duration: 200), 200, "查歌词时长: 普通曲目照报")

    let t0 = Date(timeIntervalSince1970: 1_790_400_000)
    expectEqual(P.freshResumeSignalAge(signalAt: t0, now: t0.addingTimeInterval(0.4)).map { abs($0 - 0.4) < 0.001 }, true,
                "恢复信号: 刚到的信号(0.4s)照用")
    expectEqual(P.freshResumeSignalAge(signalAt: t0, now: t0.addingTimeInterval(40)), nil,
                "恢复信号: 靠轮询发现恢复时手上是暂停那一刻的旧信号 → 当没拿到,不拿它砍起点")
    expectEqual(P.freshResumeSignalAge(signalAt: nil, now: t0), nil, "恢复信号: 没有信号")
}

/// 在播但头一拍没有时长(网页 / 汽水换歌常这样):那一拍不记账,下一拍有了时长按换歌处理 —— 学到的锚点滞后照样预置。
@MainActor
private func replayNoDurationFirstTick() {
    let suite = "lyrimuse-selftest.playback-source.no-duration"
    let defaults = UserDefaults(suiteName: suite)!
    defaults.removePersistentDomain(forName: suite)
    defer { defaults.removePersistentDomain(forName: suite) }
    let env = PlaybackPositionEnvironment(
        defaults: defaults, readPositionBias: { nil }, writePositionBias: { _ in }, outputRoute: { nil },
        browserProbeTrackChanged: { _, _ in }, browserProbeKick: { _, _, _ in }, browserProbeConsume: { _, _, _ in nil },
        browserProbeReopenAfterResume: { _ in }, spotifyProbeTrackChanged: { _, _ in }, spotifyProbeConsume: { _, _, _ in nil },
        spotifyProbeRequestConfirmation: { _ in }, latestAnchorPublishedWhilePaused: { false })
    let source = LocalPlaybackSource.makeForPositionReplay(environment: env)
    let soda = PlaybackPlayer.soda.bundleIdentifier
    let t0 = Date(timeIntervalSince1970: 1_790_300_000)
    func at(_ s: Double) -> Date { t0.addingTimeInterval(s) }
    func snap(_ title: String, anchor: Double, elapsed: Double, duration: Double?) -> MediaControlSnapshot {
        .forReplay(title: title, artist: "Fujii Kaze", album: "Prema", duration: duration, elapsedTime: elapsed,
                   playing: true, playbackRate: 1, bundleIdentifier: soda, anchorElapsedTime: anchor)
    }
    // 先学到汽水的锚点滞后 0.43(同 PositionReplayTests 的 replaySodaAnchorLag)。
    let lag = 0.43
    for t in stride(from: 0.0, through: 98, by: 2) {
        source.replayPosition(snap("My Anata", anchor: 0, elapsed: 0.3 + t, duration: 104), now: at(t))
    }
    source.replayPosition(snap("My Anata", anchor: 100.3 + lag, elapsed: 100.3 + lag, duration: 104), now: at(100))
    // 下一首头一拍没有时长。
    source.replayPosition(snap("情话", anchor: 0, elapsed: 0.2, duration: nil), now: at(104.2))
    expectEqual(source.replayPositionMs(at: at(104.2)), nil, "缺时长那一拍: 上一首的锚点拿掉,不拿它外推新歌")
    source.replayPosition(snap("情话", anchor: 0, elapsed: 2.2, duration: 200), now: at(106.2))
    let shown = source.replayPositionMs(at: at(106.2)).map { Double($0) / 1000 }
    expectEqual(shown.map { abs($0 - (2.2 + lag)) <= 0.02 }, true,
                "缺时长那一拍: 下一拍有了时长照换歌处理、预置学到的 +0.43(屏上 \(shown.map { String(format: "%.3f", $0) } ?? "nil"))")
}

// ---- 最近记录:第 N 次听 / 拼页 / 单条 track ----

@MainActor
private func recentListensTests() {
    typealias O = RecentPlayOrdinal
    let key: (String, String) -> String = { "\($0)|\($1)" }
    let totals = ["A|循环": 21, "B|别的": 5]
    let page2 = [(artist: "A", title: "循环"), (artist: "B", title: "别的")]
    let page1 = Array(repeating: (artist: "A", title: "循环"), count: 3)
    expectEqual(O.ordinals(rows: page2, totals: totals, playCountKey: key, preceding: page1), [18, 5],
                "第 N 次听: 第 2 页要减掉第 1 页里更新的同曲收听(21 − 3 = 18,不是 21)")
    expectEqual(O.ordinals(rows: page2, totals: totals, playCountKey: key, preceding: nil), [nil, nil],
                "第 N 次听: 前几页拼不齐 → 这一页不显示次数")
    expectEqual(O.ordinals(rows: page2, totals: totals, playCountKey: key), [21, 5], "第 N 次听: 第 1 页(默认)照旧")

    typealias C = LastfmPageComposer
    let a = C.Source(firstPosition: 0, rows: Array(0..<50))
    let b = C.Source(firstPosition: 40, rows: Array(40..<60))
    expectEqual(C.composeRange(lo: 0, hi: 60, sources: [a, b], identity: { String($0) }), Array(0..<60),
                "拼页: 0..<60 由 feed 50 行 + 第 3 页缓存拼齐")
    expectEqual(C.composeRange(lo: 0, hi: 70, sources: [a, b], identity: { String($0) }), nil, "拼页: 缺位置就拼不齐")
    expectEqual(C.lateInsertDetected(previousUTS: [300, 200, 100], currentUTS: [400, 300, 200, 100]), false,
                "插入检测: 新 scrobble 在最上面不算插入")
    expectEqual(C.lateInsertDetected(previousUTS: [300, 200, 100], currentUTS: [300, 250, 200, 100]), true,
                "插入检测: 比旧 feed 最新那条还旧的新记录(回填 / 手机补交)= 插进了中间")
    expectEqual(C.lateInsertDetected(previousUTS: [], currentUTS: [1, 2]), false, "插入检测: 头一份 feed 没有可比的")

    let single: [String: Any] = ["recenttracks": ["track": ["name": "唯一一条", "artist": ["#text": "A"],
                                                           "date": ["uts": "1790000000"]]]]
    expectEqual(LastfmRecentRows.parse(single).map(\.title), ["唯一一条"], "最近记录: 只有一条时 track 是对象,也要认")

    let existing: [(date: Date, album: String?)] = [(Date(timeIntervalSince1970: 300), nil), (Date(timeIntervalSince1970: 200), nil)]
    let nextPage: [(date: Date, album: String?)] = [(Date(timeIntervalSince1970: 200), nil), (Date(timeIntervalSince1970: 100), nil)]
    let merged = PlayCountBreakdownMath.appendingPage(existing: existing, page: nextPage, previousTotal: 10, newTotal: 11)
    expectEqual(merged.map { $0.date.timeIntervalSince1970 }, [300, 200, 100],
                "计次明细补页: 两页之间多了 1 次收听,新一页开头重复的那一行去掉")
    let noShift = PlayCountBreakdownMath.appendingPage(existing: existing, page: nextPage, previousTotal: 10, newTotal: 10)
    expectEqual(noShift.count, 4, "计次明细补页: 总数没变就不去重(同一秒的真实双端重复要留着)")

    expectEqual(LastfmEditorialInfo.cleaned("前半段 <a href=\"x\">某歌手</a> 后半段 <a href=\"https://last.fm\">Read more on Last.fm</a>. License"),
                "前半段 某歌手 后半段", "简介: 正文中间的链接只去掉标签,从最后一个链接截")
}

// ---- 广告探针 / 跳过复核 ----

@MainActor
private func browserProbeTests() {
    typealias S = YouTubeMusicAdSkipper
    expectEqual(S.normalizedBadgeCount("赞助商广告 1/2 · 0:20"), "1/2", "徽章归一: 剔掉倒计时")
    expectEqual(S.normalizedBadgeCount("Ad 2 of 2 · 0:05"), "2/2", "徽章归一: 英文写法")
    expectEqual(S.normalizedBadgeCount("广告 · 0:20"), "", "徽章归一: 只有倒计时没有计数")
    let clicked = S.ClickResult.skippable(desc: "BUTTON.ytp-skip-ad-button", badge: "赞助商广告 1/2 · 0:20", videoTime: 5)
    expectEqual(S.adAdvanced(afterClick: clicked, verify: .still(badge: "赞助商广告 1/2 · 0:19", videoTime: 6)), false,
                "跳过复核: 只是倒计时变了,不算跳过")
    expectEqual(S.adAdvanced(afterClick: clicked, verify: .still(badge: "赞助商广告 2/2 · 0:15", videoTime: 0)), true,
                "跳过复核: 计数翻到下一条才算")

    for (name, js) in [("probe", YouTubeMusicAdProbe.probeJS), ("skip", S.skipJS), ("verify", S.verifyJS)] {
        expectEqual(js.contains("PAUSED:"), true, "多标签页: \(name) JS 给暂停的标签页加 PAUSED: 前缀")
        expectEqual(js.contains("\""), false, "多标签页: \(name) JS 里不许出现双引号")
    }
    for family in [BrowserAutomationPermission.Family.chromium, .safari] {
        let s = BrowserTabProbeScript.build(bundleID: "com.google.Chrome", family: family,
                                            hostMarker: "music.youtube.com", js: "1", eventTimeoutSeconds: 1)
        expectEqual(s.contains("r starts with \"PAUSED:\""), true, "多标签页/\(family): 暂停的那页先记成备选")
        expectEqual(s.contains("if fallback is not \"\" then return fallback"), true, "多标签页/\(family): 都找完才交回备选")
        expectEqual(s.contains("set tabCount to count of tabs of window wi\n"), true, "多标签页/\(family): 数标签页那一步包在 try 里")
    }

    let quoted = "\"0|0|0||Live at \\\"Budokan\\\"\""
    expectEqual(YouTubeMusicAdProbe.parse(quoted)?.album, "Live at \"Budokan\"",
                "专辑名: 只脱两头一对引号,里面转义的引号还原")

    var backoff = ProbeFailureBackoff()
    let t0 = Date(timeIntervalSince1970: 1_790_500_000)
    expectEqual(backoff.suppresses(key: "k", now: t0), false, "探针退避: 没失败过不挡")
    backoff.noteFailure(key: "k", now: t0)
    expectEqual(backoff.suppresses(key: "k", now: t0.addingTimeInterval(1)), true, "探针退避: 第 1 次失败后 2 秒内不再探")
    expectEqual(backoff.suppresses(key: "k", now: t0.addingTimeInterval(2.5)), false, "探针退避: 过了 2 秒再探")
    for i in 2...8 { backoff.noteFailure(key: "k", now: t0.addingTimeInterval(Double(i))) }
    expectEqual(ProbeFailureBackoff.wait(afterFailures: 8), 60, "探针退避: 封顶 60 秒")
    expectEqual(backoff.suppresses(key: "别的歌", now: t0.addingTimeInterval(9)), false, "探针退避: 换了曲目不挡")
    backoff.reset()
    expectEqual(backoff.suppresses(key: "k", now: t0.addingTimeInterval(9)), false, "探针退避: 成功后清零")
}

// ---- 资料库删除 / 缓存读取 / 退避 / 令牌 / launchd ----

@MainActor
private func libraryAndCacheTests() {
    let script = MusicPlaybackController.removeFromLibraryScript(expectedName: "某首歌")
    expectEqual(script.contains("whose persistent ID is tPID"), true, "资料库删除: 先按 persistent ID 认")
    expectEqual(script.contains("considering case"), true, "资料库删除: 兜底按元数据时区分大小写")
    expectEqual(script.contains("if (count of exact) is not 1 then error"), true, "资料库删除: 兜底必须恰好一条")
    expectEqual(script.contains("if tAlbum is \"\" then error"), true, "资料库删除: 没有专辑名不按元数据删")
    expectEqual(script.contains("if d > 1 or d < -1 then error"), true, "资料库删除: 时长对不上不删")
    expectEqual(script.contains("delete (item 1 of matches)"), false, "资料库删除: 不再删「第一条匹配」")
    expectEqual(script.contains("current track changed"), true, "资料库删除: 歌名核对还在")

    let t = Date(timeIntervalSince1970: 1_790_600_000)
    expectEqual(EnrichCacheReader.decodeAlreadyFailed(mtime: t, failedMTime: t), true, "后台解码: 同一版上次没解开 → 不再重试")
    expectEqual(EnrichCacheReader.decodeAlreadyFailed(mtime: t.addingTimeInterval(1), failedMTime: t), false, "后台解码: 文件变了再试")
    expectEqual(EnrichCacheReader.decodeAlreadyFailed(mtime: nil, failedMTime: t), false, "后台解码: 取不到 mtime 不挡")

    let now = Date(timeIntervalSince1970: 1_790_700_000)
    expectEqual(ITunesSearchBackoff.until(status: 0, retryAfter: nil, now: now, current: nil),
                now.addingTimeInterval(ITunesSearchBackoff.forbiddenCooldown), "iTunes 退避: 网络层失败(0)按 30 秒,同 collector")
    expectEqual(MusicCatalogSearch.searchURL(title: "1+1", artist: "Beyoncé", storefront: "us")?.absoluteString.contains("1%2B1"), true,
                "iTunes 搜索: `+` 要编码,不然被当成空格")

    let oldFormat = try! JSONSerialization.data(withJSONObject: ["media_user_token": "x", "rejected_at": 1_790_000_000])
    expectEqual(AppleMusicTokenFile.parse(oldFormat, fileDate: Date(timeIntervalSince1970: 1_790_000_000.6))?.rejected, true,
                "Apple Music 令牌: 老格式(没有 saved_at)有 rejected_at 就是被拒了")
    let newFormat = try! JSONSerialization.data(withJSONObject: ["media_user_token": "x", "saved_at": 1_790_000_100, "rejected_at": 1_790_000_000])
    expectEqual(AppleMusicTokenFile.parse(newFormat, fileDate: Date())?.rejected, false,
                "Apple Music 令牌: 被拒之后重新登录过(saved_at 更晚)不算失效")

    let spawnScheduled = """
    gui/502/com.lyrimuse.collector = {
    \tactive count = 0
    \tstate = spawn scheduled
    \tlast exit code = 2
    }
    """
    expectEqual(LaunchdPrintParser.parse(printExitCode: 0, printOutput: spawnScheduled), .registeredNotRunning(lastExitCode: 2),
                "launchd: 崩溃后排着重启(spawn scheduled)= 注册着、此刻没在跑")
}
