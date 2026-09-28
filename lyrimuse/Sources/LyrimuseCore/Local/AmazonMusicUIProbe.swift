import ApplicationServices
import Foundation

/// 读 Amazon Music 界面上的播放时间,拿它校准自动连播的提前量(见 `AmazonMusicPlayhead` 头注)。
///
/// 界面上那两个时间(已播 `02:24`、剩余 `-01:05`)按真正出声走(引擎每 100 毫秒推一次进度给界面,
/// `setTrackProgressNotificationPeriod ( 100 )`),是唯一能拿到的真值。Amazon Music 是 CEF 应用,网页内容的辅助功能树
/// 不跟着界面刷新,注册观察者也不推送;**只有 `AXEnhancedUserInterface` 从 false 设成 true 那一下会整棵重建一次**
/// (实测约 230 毫秒建好,期间读是空的,偶尔先读到上次那棵旧树;已经是 true 时再设 true 不刷新)。
///
/// 所以每次「关 → 开 → 等建好 → 读」得到一个读数:它对应的时刻落在切换到建好之间,读到的是整秒数。每个读数给这首的
/// 真实起点划出一个区间(见 `originInterval`),读几次取交集,窄到 `targetWidth` 就停,取中点(`settledOrigin`)。
/// 交集变空(界面时间停了一下又走,常见于刚恢复播放)就丢掉之前的读数,从最新这次重新收。
///
/// 只读属性、不做任何动作;结束时把 `AXEnhancedUserInterface` 设回 false。需要「辅助功能」权限,没有就不读
/// (调用方退回不校准)。剩余时间用来核对读到的是当前这首:已播 + 剩余要跟时长对得上。
///
/// 自动连播切歌时界面跟日志同一刻换到下一首,但时间停在 `00:00` 直到真正出声才走;所以 0 秒的读数不用,
/// 否则几次停住的 0 会收敛出一个偏早的起点。
public enum AmazonMusicUIProbe {
    public struct Sample: Equatable, Sendable {
        /// 切换开关的时刻与树建好、读到数的时刻。
        public let toggledAt: Date
        public let readAt: Date
        /// 读到的已播秒数。
        public let seconds: Int

        public init(toggledAt: Date, readAt: Date, seconds: Int) {
            self.toggledAt = toggledAt
            self.readAt = readAt
            self.seconds = seconds
        }
    }

    /// 界面文字比真实位置最多晚这么多更新(引擎推进度的周期)。
    public static let displayLag: TimeInterval = 0.1
    /// 交集窄到这个宽度就停。下限由读数本身定:每次读数对应的时刻在「切换到建好」这约 0.23 秒里不确定,界面文字又最多晚
    /// `displayLag`,交集最窄也有 0.33 秒左右 —— 别把门槛设到这个量以下,否则永远收不住。
    public static let targetWidth: TimeInterval = 0.45
    /// 最多读这么久。
    public static let timeout: TimeInterval = 4
    /// 一次重建最多等这么久。
    public static let rebuildTimeout: TimeInterval = 1

    /// 一组读数推出的「这首真实起点」区间(epoch 秒,位置 = 当前时刻 − 起点)。交集为空(中途暂停、读错)返回 nil。纯函数。
    ///
    /// 读数 v 在 [切换, 建好] 之间某一刻 t 取得,那一刻真实位置落在 [v, v + 1 + displayLag) → 起点 ∈ (切换 − v − 1 − displayLag, 建好 − v]。
    public static func originInterval(_ samples: [Sample]) -> ClosedRange<Double>? {
        guard !samples.isEmpty else { return nil }
        var lo = -Double.infinity
        var hi = Double.infinity
        for s in samples {
            let v = Double(s.seconds)
            lo = max(lo, s.toggledAt.timeIntervalSince1970 - v - 1 - displayLag)
            hi = min(hi, s.readAt.timeIntervalSince1970 - v)
        }
        return lo < hi ? lo...hi : nil
    }

    /// 一组读数收得住就给起点(区间中点):交集窄到 `targetWidth`,而且读数里至少跨过一次整秒跳变 —— 界面时间停着
    /// (刚恢复、刚切歌还没出声)时几次同样的读数也能把区间收窄,但收出的起点是错的。收不住返回 nil。纯函数。
    public static func settledOrigin(_ samples: [Sample]) -> Double? {
        guard let range = originInterval(samples), range.upperBound - range.lowerBound <= targetWidth,
              Set(samples.map(\.seconds)).count >= 2 else { return nil }
        return (range.lowerBound + range.upperBound) / 2
    }

    /// 解析界面上的时间:`02:24` / `1:02:03`;带负号的是剩余时间。认不出返回 nil。纯函数,selftest 直接覆盖。
    public static func parseClock(_ text: String) -> (seconds: Int, negative: Bool)? {
        var s = text.trimmingCharacters(in: .whitespaces)
        let negative = s.hasPrefix("-") || s.hasPrefix("−")
        if negative { s.removeFirst() }
        let parts = s.split(separator: ":", omittingEmptySubsequences: false)
        guard (2...3).contains(parts.count),
              parts.allSatisfy({ !$0.isEmpty && $0.count <= 2 && $0.allSatisfy(\.isNumber) }) else { return nil }
        let nums = parts.compactMap { Int($0) }
        guard nums.count == parts.count, nums.dropFirst().allSatisfy({ $0 < 60 }) else { return nil }
        let seconds = nums.reduce(0) { $0 * 60 + $1 }
        return (seconds, negative)
    }

    /// 已播 + 剩余跟时长差多少还算同一首(界面取整、元数据时长取整)。
    public static let durationTolerance: Double = 3

    /// 一组读数是不是当前这首的进度条。纯函数。
    public static func matchesTrack(elapsed: Int, remaining: Int?, duration: Double?) -> Bool {
        guard let remaining, let duration, duration > 0 else { return true }
        return abs(Double(elapsed + remaining) - duration) <= durationTolerance
    }

    /// 读不出起点的原因(写进日志)。
    public enum Failure: String, Sendable {
        case notTrusted = "no accessibility permission"
        case noClock = "no clock text in the accessibility tree"
        case otherTrack = "the clock on screen belongs to another track"
        case inconsistent = "readings do not advance with time (paused or seeking?)"
        case timedOut = "did not narrow down in time"
    }

    /// 同步读,阻塞最多 `timeout`,返回这首的真实起点(epoch 秒)。别在主线程调。
    public static func sampleOrigin(pid: pid_t, duration: Double?) -> Result<Double, FailureBox> {
        guard AXIsProcessTrusted() else { return .failure(.init(.notTrusted, samples: 0)) }
        let durationText = duration.map { String(Int($0)) } ?? "?"
        let app = AXUIElementCreateApplication(pid)
        defer { AXUIElementSetAttributeValue(app, "AXEnhancedUserInterface" as CFString, kCFBooleanFalse) }
        var samples: [Sample] = []
        var restarts = 0
        var otherTrack: String?
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            AXUIElementSetAttributeValue(app, "AXEnhancedUserInterface" as CFString, kCFBooleanFalse)
            let toggledAt = Date()
            AXUIElementSetAttributeValue(app, "AXEnhancedUserInterface" as CFString, kCFBooleanTrue)
            var pairs: [ClockPair] = []
            while pairs.isEmpty, Date().timeIntervalSince(toggledAt) < rebuildTimeout {
                pairs = readClocks(app)
                if pairs.isEmpty { Thread.sleep(forTimeInterval: 0.02) }
            }
            let readAt = Date()
            guard !pairs.isEmpty else { continue }
            // 开关之后头一次读到的偶尔还是上次那棵旧树(上一首的时间),跳过接着读,别整次放弃。
            guard let (elapsed, _) = pickPair(pairs, duration: duration) else {
                otherTrack = pairs.map { "\($0.elapsed)+\($0.remaining.map(String.init) ?? "?")" }.joined(separator: ",")
                Thread.sleep(forTimeInterval: 0.1)
                continue
            }
            if elapsed == 0 {
                Thread.sleep(forTimeInterval: 0.1)
                continue
            }
            samples.append(Sample(toggledAt: toggledAt, readAt: readAt, seconds: elapsed))
            if originInterval(samples) == nil {
                restarts += 1
                samples = [samples.last!]
            }
            if let origin = settledOrigin(samples) { return .success(origin) }
            Thread.sleep(forTimeInterval: 0.05)
        }
        if samples.isEmpty, let otherTrack {
            return .failure(.init(.otherTrack, samples: 0, detail: "clocks \(otherTrack) vs duration \(durationText)"))
        }
        let reason: Failure = samples.isEmpty ? .noClock : (restarts > 0 ? .inconsistent : .timedOut)
        return .failure(.init(reason, samples: samples.count, detail: restarts > 0 ? "restarted \(restarts)x" : nil))
    }

    public struct FailureBox: Error, Sendable {
        public let reason: Failure
        public let samples: Int
        /// 读到了什么(写进日志排查)。
        public let detail: String?
        public init(_ reason: Failure, samples: Int, detail: String? = nil) {
            self.reason = reason
            self.samples = samples
            self.detail = detail
        }
    }

    /// 树里挨在一起的一对已播 / 剩余时间。剩余缺了是 nil。
    public struct ClockPair: Equatable, Sendable {
        public let elapsed: Int
        public let remaining: Int?
        public init(elapsed: Int, remaining: Int?) {
            self.elapsed = elapsed
            self.remaining = remaining
        }
    }

    /// 已播后面隔多少个节点之内的剩余时间算同一对(进度条两端的两段文字在树里相隔两个节点)。
    static let pairDistance = 4

    /// 按树的遍历顺序把时间文字配成对:每个已播配它后面 `pairDistance` 个节点之内的第一个剩余。纯函数。
    public static func pairClocks(_ clocks: [(index: Int, seconds: Int, negative: Bool)]) -> [ClockPair] {
        var out: [ClockPair] = []
        for (i, c) in clocks.enumerated() where !c.negative {
            let next = clocks.dropFirst(i + 1).first { $0.negative && $0.index - c.index <= pairDistance }
            out.append(ClockPair(elapsed: c.seconds, remaining: next?.seconds))
        }
        return out
    }

    /// 从几对里挑当前这首的进度条:第一对带剩余、对得上时长的;整棵树都没有剩余时间才退回第一个已播。
    /// 有剩余却都对不上返回 nil。纯函数。
    public static func pickPair(_ pairs: [ClockPair], duration: Double?) -> (elapsed: Int, remaining: Int?)? {
        let full = pairs.filter { $0.remaining != nil }
        let pick = full.isEmpty
            ? pairs.first
            : full.first { matchesTrack(elapsed: $0.elapsed, remaining: $0.remaining, duration: duration) }
        return pick.map { ($0.elapsed, $0.remaining) }
    }

    private static func attr(_ e: AXUIElement, _ name: String) -> AnyObject? {
        var v: AnyObject?
        return AXUIElementCopyAttributeValue(e, name as CFString, &v) == .success ? v : nil
    }

    /// 树里所有配成对的时间文字;树还没建好返回空。
    private static func readClocks(_ app: AXUIElement) -> [ClockPair] {
        var clocks: [(index: Int, seconds: Int, negative: Bool)] = []
        var visited = 0
        func walk(_ e: AXUIElement, _ depth: Int) {
            visited += 1
            guard depth <= 40, visited <= 5000 else { return }
            if let s = attr(e, kAXValueAttribute) as? String, let c = parseClock(s) {
                clocks.append((visited, c.seconds, c.negative))
            }
            if let kids = attr(e, kAXChildrenAttribute) as? [AXUIElement] {
                for k in kids { walk(k, depth + 1) }
            }
        }
        walk(app, 0)
        return pairClocks(clocks)
    }
}
