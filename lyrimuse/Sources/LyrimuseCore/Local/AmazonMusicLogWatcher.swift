import Foundation
import os

/// 盯着 Amazon Music 的日志,把里面的播放事件实时喂给 `AmazonMusicPlayhead`,并在两层之间给出这一拍的位置。
///
/// - 第一次用到时(见到第一份 Amazon Music 快照)才开始:先重放日志末尾一段,把当前这首的状态找回来(App 在歌曲
///   中途重启也接得上),之后挂文件事件,有新行就读。
/// - 实时读到的行按「读到的那一刻」计时,但不晚于行首那一秒的末尾:读晚了(文件事件漏了、下一拍轮询才补读)
///   误差也不超过一秒。
/// - 暂停 / 恢复 / 拖动 / 开播 / 卡顿都会调 `onPlaybackEvent`,调用方借它立刻补一次轮询:拖动在系统 Now Playing
///   里没有任何通知,不靠这里就要等下一拍。
/// - 文件变短(Amazon Music 重启后重写)或被换掉就从头读。
/// - 自动连播开头的那首(`AmazonMusicPlayhead.needsLeadCalibration`)开播 `calibrationDelay` 之后,在后台读一次 Amazon 界面上的
///   播放时间校准提前量(`AmazonMusicUIProbe`);没有辅助功能权限就不校准。读不到隔 `calibrationRetry` 再试,最多
///   `calibrationMaxAttempts` 次。校准结果写进 `AmazonMusicLeadFile` 给 collector。
///
/// 规则本身在 `AmazonMusicPlayhead`(纯函数),这里只管读文件和记账。
public final class AmazonMusicLogWatcher: @unchecked Sendable {
    public static let shared = AmazonMusicLogWatcher()

    private static let logger = Logger(subsystem: "me.yudaotor.lyrimuse", category: "amazon-music")
    /// 第一次读只看末尾这么多字节:一份日志一天能长到几 MB,当前这首的开播一定在最后这段里。
    static let initialTailBytes = 256 << 10
    /// 日志不存在(没装 / 还没启动)时隔多久再看一眼。
    static let missingRetry: TimeInterval = 10
    /// 开播后隔多久开始校准。自动连播时开播后头几秒还在放上一首的尾巴,界面停在 `00:00`,探针自己等它走起来
    /// (`AmazonMusicUIProbe.startWait`);这里只让界面先换到这一首。没有前奏的歌开口就唱,别把它调回几秒。
    static let calibrationDelay: TimeInterval = 1
    /// 卡顿(含暂停后恢复跟着的那次)平息后隔多久重新校准。探针自己会丢掉界面停住那几次的读数,不用等太久。
    static let stallSettleDelay: TimeInterval = 0.5
    static let calibrationRetry: TimeInterval = 5
    static let calibrationMaxAttempts = 3

    /// 播放事件回调(暂停 = true 表示这一下可能让播放停了)。在私有队列上调用。
    public nonisolated(unsafe) static var onPlaybackEvent: (@Sendable (_ pause: Bool) -> Void)?

    private let queue = DispatchQueue(label: "me.yudaotor.lyrimuse.amazon-music-log")
    private let lock = NSLock()
    private let path: String

    // 以下只在 queue 上读写。
    private var handle: FileHandle?
    private var source: DispatchSourceFileSystemObject?
    private var offset: UInt64 = 0
    private var partial = Data()
    private var started = false

    // 以下在 lock 里读写。
    private var state = AmazonMusicPlayhead.State()
    private var seenEvent = false
    private var fileAvailable = false
    private var timer: AmazonMusicPlayhead.SelfTimer?
    private var lastSource: AmazonMusicPlayhead.Source?
    private var calibrating = false
    /// 这首(曲目 + 开播时刻)试过几次、上次是什么时候。
    private var calibrationAttempts: (key: String, count: Int, lastAt: Date)?
    private let calibrationQueue = DispatchQueue(label: "me.yudaotor.lyrimuse.amazon-music-ui")

    public init(path: String = AmazonMusicLogWatcher.defaultPath) {
        self.path = path
    }

    public static var defaultPath: String {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Application Support/Amazon Music/Logs/AmazonMusic.log").path
    }

    /// 幂等。见到 Amazon Music 的快照时调用。
    public func ensureStarted() {
        queue.async { [self] in
            guard !started else { return }
            started = true
            open(replay: true)
        }
    }

    /// 这一拍的位置。`pauseObservedAt` 是 stream watcher 记下的最近一次暂停时刻(自记时层用它把暂停落在
    /// 真正发生的那一刻,而不是这一拍轮询的时刻)。
    public func reading(trackKey: String, metadataTimestamp: Date?, playing: Bool, pauseObservedAt: Date?,
                        now: Date, pid: pid_t? = nil, duration: Double? = nil) -> AmazonMusicPlayhead.Reading {
        queue.sync { drain(live: true) }
        lock.lock()
        defer { lock.unlock() }
        // 起点换成系统时间戳这一步记进状态,别只在出位置时临时换:之后到的暂停 / 卡顿按状态里的起点记停表位置,
        // 两边起点不同(实测差近 1 秒)暂停那一下就跳一截。
        if AmazonMusicPlayhead.logCovers(state, metadataTimestamp: metadataTimestamp) {
            state = AmazonMusicPlayhead.calibrated(state, metadataTimestamp: metadataTimestamp)
        }
        var observedAt = now
        if !playing, let pauseObservedAt, now.timeIntervalSince(pauseObservedAt) < 3,
           pauseObservedAt <= now { observedAt = pauseObservedAt }
        timer = AmazonMusicPlayhead.advance(timer, trackKey: trackKey, metadataTimestamp: metadataTimestamp,
                                            playing: playing, observedAt: observedAt)
        let log = fileAvailable && seenEvent ? state : nil
        let r = AmazonMusicPlayhead.reading(log: log, timer: timer!, metadataTimestamp: metadataTimestamp, now: now)
        if !r.staleMetadata, r.source != lastSource {
            lastSource = r.source
            Self.logger.notice("amazon music clock: position from \(r.source.rawValue, privacy: .public)")
        }
        if r.source == .log, playing, let pid { scheduleCalibrationLocked(pid: pid, duration: duration, metadataTimestamp: metadataTimestamp, now: now) }
        return r
    }

    // MARK: - 自动连播提前量的界面校准(lock 里调)

    private func scheduleCalibrationLocked(pid: pid_t, duration: Double?, metadataTimestamp: Date?, now: Date) {
        guard !calibrating, AmazonMusicPlayhead.needsLeadCalibration(state), let id = state.trackID,
              let startedAt = state.trackStartedAt, now.timeIntervalSince(startedAt) >= Self.calibrationDelay,
              state.lastStallAt.map({ now.timeIntervalSince($0) >= Self.stallSettleDelay }) ?? true else { return }
        // 卡顿之后的那次重新校准另算次数(键带上最近一次卡顿的时刻)。
        let key = id + "@" + String(startedAt.timeIntervalSince1970) + "#" + String(state.lastStallAt?.timeIntervalSince1970 ?? 0)
        if let a = calibrationAttempts, a.key == key {
            guard a.count < Self.calibrationMaxAttempts, now.timeIntervalSince(a.lastAt) >= Self.calibrationRetry else { return }
            calibrationAttempts = (key, a.count + 1, now)
        } else {
            calibrationAttempts = (key, 1, now)
        }
        calibrating = true
        let stallBefore = state.lastStallAt
        let timelineOrigin = AmazonMusicPlayhead.engineTimelinePosition(state, at: now).map { now.addingTimeInterval(-$0) }
        calibrationQueue.async { [self] in
            let result = AmazonMusicUIProbe.sampleOrigin(pid: pid, duration: duration, timelineOrigin: timelineOrigin) { [self] in
                lock.lock()
                defer { lock.unlock() }
                return state.trackID == id && state.trackStartedAt == startedAt
            }
            let origin = try? result.get()
            lock.lock()
            calibrating = false
            guard state.trackID == id, state.trackStartedAt == startedAt else {
                lock.unlock()
                return
            }
            // 读界面期间又卡顿 / 暂停过:读数跨了两段时间轴,这次作废,平息后重来(新卡顿另算次数)。
            guard state.lastStallAt == stallBefore, !state.paused else {
                lock.unlock()
                Self.logger.notice("amazon music lead: calibration discarded: playback stalled or paused while reading the screen")
                return
            }
            // 起点换算成「某一刻界面该显示的整秒」,交给纯函数算提前量:取现在这一刻、真实位置向下取整的那个秒边界。
            let now = Date().timeIntervalSince1970
            let edge = origin.map { o -> (Date, Int) in
                let secs = Int((now - o).rounded(.down))
                return (Date(timeIntervalSince1970: o + Double(secs)), secs)
            }
            guard let edge,
                  let done = AmazonMusicPlayhead.calibratingLead(
                    AmazonMusicPlayhead.calibrated(state, metadataTimestamp: metadataTimestamp),
                    edgeAt: edge.0, displaySeconds: edge.1) else {
                lock.unlock()
                let why: String
                switch result {
                case .failure(let f): why = "\(f.reason.rawValue) after \(f.samples) readings" + (f.detail.map { " (\($0))" } ?? "")
                case .success(let o): why = String(format: "lead out of range (origin %.3f)", o)
                }
                Self.logger.notice("amazon music lead: calibration failed: \(why, privacy: .public)")
                return
            }
            state.audibleLead = done.audibleLead
            state.leadCalibrated = true
            state.stalledSinceCalibration = false
            let lead = done.audibleLead
            lock.unlock()
            Self.logger.notice("amazon music lead: log clock is \(lead, format: .fixed(precision: 2))s ahead of the audio, subtracting it (ui=\(edge.1, privacy: .public)s natural=\(done.startedNaturally, privacy: .public) origin=\(edge.0.timeIntervalSince1970 - Double(edge.1), format: .fixed(precision: 3)))")
            AmazonMusicLeadFile.write(.init(trackID: id, startedAtMs: Int64(startedAt.timeIntervalSince1970 * 1000),
                                            leadSecs: lead, writtenAtMs: Int64(Date().timeIntervalSince1970 * 1000)))
            Self.onPlaybackEvent?(false)
        }
    }

    // MARK: - 读文件(queue 上)

    private func open(replay: Bool) {
        source?.cancel()
        source = nil
        try? handle?.close()
        handle = nil
        guard let h = FileHandle(forReadingAtPath: path) else {
            setAvailable(false)
            queue.asyncAfter(deadline: .now() + Self.missingRetry) { [weak self] in
                guard let self, self.handle == nil else { return }
                self.open(replay: true)
            }
            return
        }
        handle = h
        setAvailable(true)
        let size = (try? h.seekToEnd()) ?? 0
        offset = replay && size > UInt64(Self.initialTailBytes) ? size - UInt64(Self.initialTailBytes) : 0
        partial = Data()
        lock.lock()
        state = AmazonMusicPlayhead.State()
        seenEvent = false
        lock.unlock()
        drain(live: !replay)
        let src = DispatchSource.makeFileSystemObjectSource(
            fileDescriptor: h.fileDescriptor, eventMask: [.extend, .write, .delete, .rename], queue: queue)
        src.setEventHandler { [weak self] in
            guard let self else { return }
            let flags = src.data
            if flags.contains(.delete) || flags.contains(.rename) {
                self.open(replay: false)
            } else {
                self.drain(live: true)
            }
        }
        source = src
        src.resume()
    }

    private func setAvailable(_ available: Bool) {
        lock.lock()
        fileAvailable = available
        lock.unlock()
    }

    /// 把新写的部分读完并推进状态。`live` = 这些行是刚写的(按读到的时刻计),否则是历史(按行首 + 0.5)。
    private func drain(live: Bool) {
        guard let handle else { return }
        let size = (try? handle.seekToEnd()) ?? offset
        if size < offset {
            // Amazon Music 重启后重写了日志:从头读,这些行都是刚写的。
            offset = 0
            partial = Data()
            lock.lock()
            state = AmazonMusicPlayhead.State()
            seenEvent = false
            lock.unlock()
        }
        guard size > offset else { return }
        try? handle.seek(toOffset: offset)
        guard let chunk = try? handle.read(upToCount: Int(size - offset)), !chunk.isEmpty else { return }
        offset += UInt64(chunk.count)
        var data = partial
        data.append(chunk)
        guard let last = data.lastIndex(of: UInt8(ascii: "\n")) else {
            partial = data
            return
        }
        partial = Data(data[data.index(after: last)...])
        let text = String(decoding: data[..<last], as: UTF8.self)
        let readAt = Date()
        var events: [AmazonMusicPlayhead.Event] = []
        lock.lock()
        for line in text.split(separator: "\n", omittingEmptySubsequences: true) {
            guard let (lineTime, event) = AmazonMusicPlayhead.parse(line: String(line)) else { continue }
            let at = live
                ? min(readAt, lineTime.addingTimeInterval(1))
                : lineTime.addingTimeInterval(AmazonMusicPlayhead.replayedLineOffset)
            state = AmazonMusicPlayhead.apply(event, at: max(at, lineTime), to: state)
            seenEvent = true
            events.append(event)
        }
        lock.unlock()
        guard live, !events.isEmpty, let onPlaybackEvent = Self.onPlaybackEvent else { return }
        onPlaybackEvent(events.contains(.paused) || events.contains(.stall(true)))
    }
}

/// App → collector:自动连播那首校准出的提前量(collector 读不了界面,见 AmazonMusicUIProbe)。跟 Go 侧 amazonmusic.go
/// `amazonLeadFileName` 逐字节一致,字段名同 json tag。只对同一首(日志曲目标识 + 开播时刻)生效。
public struct AmazonMusicLeadRecord: Codable, Equatable, Sendable {
    public var trackID: String
    public var startedAtMs: Int64
    public var leadSecs: Double
    public var writtenAtMs: Int64

    enum CodingKeys: String, CodingKey {
        case trackID = "track_id"
        case startedAtMs = "started_at_ms"
        case leadSecs = "lead_secs"
        case writtenAtMs = "written_at_ms"
    }
}

public enum AmazonMusicLeadFile {
    public static let fileName = "lyrimuse-amazon-lead.json"
    public static var url: URL { LyrimusePaths.configFile(fileName) }

    public static func write(_ record: AmazonMusicLeadRecord) {
        let enc = JSONEncoder()
        enc.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        guard let data = try? enc.encode(record) else { return }
        try? data.write(to: url, options: .atomic)
    }
}
