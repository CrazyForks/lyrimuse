import Foundation

/// Last.fm 统计页「听得最多」卡片的「显示更多」档位:10 → 25 → 50,全部露出后按钮变「收起」、回到 10 行。
/// 榜单一次取 `fetchLimit` 条;展开的行数不记住,每次打开设置页都从 10 行开始。
public enum ChartVisibleRows {
    public static let initial = 10
    public static let fetchLimit = 50
    static let steps = [10, 25, 50]

    /// 按钮点了之后要露出的行数。nil = 已经全部露出,按钮是「收起」。
    public static func nextTarget(current: Int, total: Int) -> Int? {
        let cap = min(fetchLimit, total)
        guard current < cap else { return nil }
        let step = steps.first { $0 > current } ?? fetchLimit
        return min(step, cap)
    }

    /// 点一次按钮之后露出几行:下一档,或者全部露出时收回 `initial`。
    public static func next(after current: Int, total: Int) -> Int {
        nextTarget(current: current, total: total) ?? initial
    }

    /// 要不要显示这个按钮:条数不超过 10 行时没有可展开的。
    public static func hasMore(total: Int) -> Bool {
        total > initial
    }
}

/// 歌手榜展开行:一位歌手在这个时段里听得最多的歌。来自 collector `artist-tracks`(见 artisttracks.go),
/// 那边按歌手榜同一套合并规则把歌曲署名归到榜上显示的名字下。
public struct ArtistTopTracks: Equatable, Codable, Sendable {
    public struct Track: Equatable, Codable, Sendable {
        public let name: String
        /// 这首歌的原始署名。合唱(「Prince & The Revolution」)时跟榜上的歌手名不同。
        public let artist: String
        public let playCount: Int
    }

    public let tracks: [Track]
    /// 这段时间听过的不同歌曲数。只有 `ArtistTracksBatch.partial` 为 false 时可用。
    public let trackCount: Int
    public let playCount: Int

    /// 署名是不是合唱串(要在歌名后面标出完整署名)。只是繁简 / 大小写不同的单人写法不算。
    public static func isCollaboration(_ credit: String) -> Bool {
        let lowered = " " + credit.lowercased() + " "
        for sep in [" & ", " and ", " x ", " with ", " feat. ", " feat ", " ft. ", ",", "、", "/"] where lowered.contains(sep) {
            return true
        }
        return false
    }
}

/// `artist-tracks -progress` 输出的一行。partial = 只含歌曲榜第 1 页:每位歌手的歌是完整结果的前几首
/// (歌曲榜按次数降序,后面的页不会有次数更高的歌),但 trackCount / playCount 不能用。
public struct ArtistTracksBatch: Equatable, Sendable {
    public let rows: [String: ArtistTopTracks]
    /// false = 分页超过上限没取完,trackCount 是下限。
    public let complete: Bool
    public let partial: Bool

    public init(rows: [String: ArtistTopTracks], complete: Bool, partial: Bool) {
        self.rows = rows
        self.complete = complete
        self.partial = partial
    }

    private struct Wire: Decodable {
        let rows: [String: ArtistTopTracks]?
        let complete: Bool?
        let partial: Bool?
    }

    /// 解析一行输出;不是约定的形状返回 nil。
    public static func parse(_ line: Data) -> ArtistTracksBatch? {
        guard let wire = try? JSONDecoder().decode(Wire.self, from: line) else { return nil }
        return ArtistTracksBatch(rows: wire.rows ?? [:], complete: wire.complete ?? false,
                                 partial: wire.partial ?? false)
    }
}

/// 歌手展开行取数的排队规则:同一时间只跑一个 artist-tracks 进程(collector 各进程的出站限速互不相干,并发几个
/// 会一起打到 Last.fm 按 IP 的限速上);跑着的时候再来的请求只留最后一个。
public struct ArtistTracksQueue: Equatable, Sendable {
    public struct Job: Equatable, Sendable {
        public let period: String
        /// true = 取全部分页;false = 只取第 1 页(预取)。
        public let full: Bool
        public let force: Bool

        public init(period: String, full: Bool, force: Bool) {
            self.period = period
            self.full = full
            self.force = force
        }
    }

    public private(set) var running: Job?
    public private(set) var queued: Job?

    public init() {}

    /// 正在跑或排着的时段(界面据此显示「正在读取」)。
    public var loadingPeriods: Set<String> {
        Set([running?.period, queued?.period].compactMap { $0 })
    }

    /// 来了一个请求(调用方已判过不新鲜)。true = 现在就跑;false = 排上了,或者正在跑的那个已经够用。
    /// 排着的跟新来的是同一个时段时,取全部分页的要求不丢。
    public mutating func submit(_ job: Job) -> Bool {
        guard let current = running else {
            running = job
            return true
        }
        if current.period == job.period, current.full || !job.full { return false }
        let keepFull = queued.map { $0.period == job.period && $0.full } ?? false
        queued = Job(period: job.period, full: job.full || keepFull, force: job.force)
        return false
    }

    /// 正在跑的那个结束了:返回排着的那个(调用方重新判新鲜再决定跑不跑),两个都清空。
    public mutating func finish() -> Job? {
        let next = queued
        running = nil
        queued = nil
        return next
    }

    public mutating func reset() {
        running = nil
        queued = nil
    }
}
