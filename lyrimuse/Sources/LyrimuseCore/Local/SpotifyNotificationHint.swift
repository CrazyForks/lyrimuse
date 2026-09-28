import Foundation

/// Spotify 原生客户端广播的 `com.spotify.client.PlaybackStateChanged` 通知里,能拿来给**这一条曲目分类**
/// 的那几个键。键名从 Spotify.app 二进制的字符串表核实过:Player State / Track ID / Name /
/// Artist / Album / Album Artist / Duration / Playback Position / Has Artwork / Popularity / Play Count /
/// Track Number / Disc Number;**没有** Artwork URL。
///
/// 用途只有一件:`Track ID` 以 `spotify:ad:` 开头就是广告 —— 跟 AppleScript `spotify url` 是同一个值,
/// 但通知在换曲那一刻就到(实测比 MediaRemote 那份 now-playing 早,广告后开播那首早了约 2.4s),而且
/// 不用 fork 子进程。标题不从这里喂状态(02 章决策 1;这是它的第二个窄例外),分类结果也只在
/// `LocalPlaybackSource.apply()` 里、按快照的歌名/歌手核对过之后才生效。
///
/// 第三个窄例外:问不到 Spotify 的 AppleScript(没授权 / 弹窗挂着)时,`Playback Position` + 收到的时刻当
/// Spotify 自己的钟用,替掉开播时晚一截的 MediaRemote 锚点(见 `MediaControlClient.spotifyNoticeReading`、02 章决策 63)。
public struct SpotifyNotificationHint: Equatable, Sendable {
    public let trackID: String
    public let name: String
    public let artist: String
    public let receivedAt: Date
    /// `Player State` 是不是 Playing(没有这个键为 nil)。
    public let playing: Bool?
    /// `Playback Position`(秒):Spotify 自己的钟在发这条通知那一刻的读数。只给问不到 AppleScript 时的位置兜底用,
    /// 见 `MediaControlClient.spotifyNoticeReading`。
    public let position: Double?

    public init(trackID: String, name: String, artist: String, receivedAt: Date = Date(),
                playing: Bool? = nil, position: Double? = nil) {
        self.trackID = trackID
        self.name = name
        self.artist = artist
        self.receivedAt = receivedAt
        self.playing = playing
        self.position = position
    }

    /// 从通知 userInfo 构造。没有 Track ID 的通知 → nil,别拿空串去分类。
    public init?(userInfo: [AnyHashable: Any]?, receivedAt: Date = Date()) {
        guard let info = userInfo, let id = info["Track ID"] as? String,
              !id.trimmingCharacters(in: .whitespaces).isEmpty
        else { return nil }
        self.init(trackID: id.trimmingCharacters(in: .whitespaces),
                  name: (info["Name"] as? String) ?? "",
                  artist: (info["Artist"] as? String) ?? "",
                  receivedAt: receivedAt,
                  playing: (info["Player State"] as? String).map { $0 == "Playing" },
                  position: (info["Playback Position"] as? NSNumber)?.doubleValue)
    }

    /// `spotify:ad:…` 是广告;其它一律不是(曲目 / 播客节目 / 本地文件)。
    public var isAd: Bool { trackID.hasPrefix("spotify:ad") }

    /// 这条提示说的是不是 media-control 快照里的这首歌。歌名逐字相等(忽略大小写与首尾空白);歌手允许一边是
    /// 另一边的前缀 —— 多歌手曲目两边的拼接写法未必一致,而歌名已经足够定身份,歌手只是防同名不同人。
    /// 任一方歌名为空 → 不匹配(空对空也不算,那不是证据)。
    public func matches(title: String?, artist snapshotArtist: String?) -> Bool {
        let a = Self.fold(name), b = Self.fold(title ?? "")
        guard !a.isEmpty, a == b else { return false }
        let x = Self.fold(artist), y = Self.fold(snapshotArtist ?? "")
        return x.isEmpty || y.isEmpty || x == y || x.hasPrefix(y) || y.hasPrefix(x)
    }

    static func fold(_ s: String) -> String {
        s.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    }
}
