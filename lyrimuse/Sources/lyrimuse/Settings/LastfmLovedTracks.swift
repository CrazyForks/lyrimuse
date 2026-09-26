import Foundation
import Combine
import LyrimuseCore

/// 账号在 Last.fm 上喜欢过的曲目,给「最近记录」每一行判「是不是已喜欢」、点心喜欢 / 取消(12 章 §8)。
///
/// 列表来自 `user.getLovedTracks`(读接口,只要 api_key + 用户名),按 `LastfmLove.lovedKey`(歌手 + 歌名,
/// 忽略大小写)对行。统计页出现时按 `ttl` 刷新;本机点的喜欢乐观地先改,写失败再改回来。歌词窗口「⋯」菜单
/// 那颗 Last.fm 喜欢(`LastfmLoveModel`)跟这里互相通知,两边显示同一个状态。只在内存里,重启后重新拉;
/// 换账号清空。
@MainActor
final class LastfmLovedTracks: ObservableObject {
    static let shared = LastfmLovedTracks()

    typealias Target = LastfmLove.Target

    @Published private(set) var keys: Set<String> = []
    /// 列表至少完整拉到过一次。没拉到之前只信本机点过的那几首,其余一律当「不知道」。
    @Published private(set) var loaded = false
    /// 能不能写:喜欢 / 取消要 session key + secret,缺了只显示、不给点。
    @Published private(set) var canWrite = false

    private var fetchedAt: Date?
    private var fetching = false
    /// 拉取那一刻读的是哪个账号;回来时账号变了就丢掉。
    private var account = ""
    /// 本机改过的状态和时刻。拉回来的列表不一定含这几下(拉取先于写落地发出,或 Last.fm 读接口还没跟上
    /// 刚才那次写),`overrideWindow` 之内以本机为准。
    private var overrides: [String: LastfmLove.LovedOverride] = [:]
    /// 写操作串行链:连点(喜欢到取消)时落地顺序必须跟点击顺序一致。
    private var writeChain: Task<Void, Never>?
    private var cancellables: Set<AnyCancellable> = []

    private static let ttl: TimeInterval = 600
    private static let overrideWindow: TimeInterval = 600

    private init() {
        account = Self.readCredentials()?.user ?? ""
        canWrite = LastfmLoveModel.credentials() != nil
        let c = ConfigStore.shared
        c.$lastfmUser.map { _ in () }
            .merge(with: c.$lastfmScrobbleUsername.map { _ in () },
                   c.$lastfmAPIKey.map { _ in () },
                   c.$lastfmScrobbleAPIKey.map { _ in () },
                   c.$lastfmScrobbleSecret.map { _ in () },
                   c.$lastfmScrobbleSessionKey.map { _ in () })
            // @Published 是 willSet 语义:回调里读到的还是旧值,推到下一拍再读。
            .receive(on: DispatchQueue.main)
            .sink { [weak self] in self?.credentialsChanged() }
            .store(in: &cancellables)
    }

    /// true = 已喜欢;false = 确定没喜欢;nil = 列表还没拉到、也不是本机刚点过的。
    func isLoved(artist: String, title: String) -> Bool? {
        guard let key = LastfmLove.lovedKey(artist: artist, title: title) else { return nil }
        if keys.contains(key) { return true }
        return loaded || overrides[key] != nil ? false : nil
    }

    func refreshIfNeeded(force: Bool = false) {
        guard !fetching, let read = Self.readCredentials() else { return }
        if !force, let fetchedAt, Date().timeIntervalSince(fetchedAt) < Self.ttl { return }
        fetching = true
        Task { [weak self] in
            let targets = await LastfmLoveAPI.fetchLovedTracks(apiKey: read.key, user: read.user)
            guard let self else { return }
            self.fetching = false
            guard let targets, read.user == self.account else { return } // 没拉到 / 期间换了账号:保持原样
            self.merge(fetched: targets)
        }
    }

    /// 翻转这一首的喜欢状态。乐观更新,写失败翻回来;写成功通知菜单那颗喜欢。
    func toggle(artist: String, title: String) {
        guard let creds = LastfmLoveModel.credentials(),
              LastfmLove.lovedKey(artist: artist, title: title) != nil else { return }
        let target = Target(artist: artist, title: title)
        let newValue = isLoved(artist: artist, title: title) != true
        apply(newValue, target: target)
        let previous = writeChain
        writeChain = Task { [weak self] in
            await previous?.value
            let ok = await LastfmLoveAPI.setLoved(newValue, target: target, creds: creds)
            guard let self else { return }
            if ok {
                LastfmLoveModel.shared.noteChanged(newValue, target: target)
            } else {
                self.apply(!newValue, target: target)
            }
        }
    }

    /// 某一首的喜欢状态已知变了(本机乐观更新 / 写成功 / 写失败回滚)。
    func apply(_ loved: Bool, target: Target) {
        guard let key = LastfmLove.lovedKey(artist: target.artist, title: target.title) else { return }
        overrides[key] = .init(loved: loved, at: Date())
        if loved { keys.insert(key) } else { keys.remove(key) }
    }

    /// 拉回来的列表 + 窗口内本机改过的状态,合并规则在 Core(`LastfmLove.mergeLoved`,selftest 钉着)。
    private func merge(fetched: [Target]) {
        let now = Date()
        let merged = LastfmLove.mergeLoved(
            fetched: Set(fetched.compactMap { LastfmLove.lovedKey(artist: $0.artist, title: $0.title) }),
            overrides: overrides, now: now, window: Self.overrideWindow)
        keys = merged.keys
        overrides = merged.overrides
        loaded = true
        fetchedAt = now
    }

    private func credentialsChanged() {
        canWrite = LastfmLoveModel.credentials() != nil
        let user = Self.readCredentials()?.user ?? ""
        guard user != account else { return }
        account = user
        keys = []
        overrides = [:]
        loaded = false
        fetchedAt = nil
    }

    /// 读喜欢列表用的账号和 api_key,跟统计页同一套(`LastfmStatsService.credentials`):没连 scrobble
    /// (没有 session key)也能读,已喜欢的心照样显示。
    private static func readCredentials() -> (user: String, key: String)? {
        let c = ConfigStore.shared
        let user = c.lastfmScrobbleUsername.isEmpty ? c.lastfmUser : c.lastfmScrobbleUsername
        let key = c.lastfmScrobbleAPIKey.isEmpty ? c.lastfmAPIKey : c.lastfmScrobbleAPIKey
        guard !user.isEmpty, !key.isEmpty else { return nil }
        return (user, key)
    }
}
