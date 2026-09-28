import Foundation

/// 浏览器页面探针「这一下没读到」(超时、自动化授权被拒、休眠标签页被 6 秒超时踢掉)之后的退避。按曲目 key 记连续失败
/// 次数,第 n 次失败后等 `min(base · 2^(n-1), cap)` 再探;成功、NOTFOUND(另有自己的免探期)、换了曲目都清零。纯值类型,
/// selftest 覆盖。
///
/// 没有它时,失败的结果不进缓存、下一拍(2 秒)照样再起一个 osascript:授权被拒或浏览器卡着的那段时间里,后台一直在
/// 派生注定失败的子进程。
public struct ProbeFailureBackoff: Equatable, Sendable {
    public static let base: TimeInterval = 2
    public static let cap: TimeInterval = 60

    public private(set) var key: String?
    public private(set) var failures = 0
    public private(set) var lastFailureAt: Date?

    public init() {}

    /// 这个 key 此刻要不要先别探。
    public func suppresses(key: String, now: Date) -> Bool {
        guard key == self.key, failures > 0, let at = lastFailureAt else { return false }
        let age = now.timeIntervalSince(at)
        return age >= 0 && age < Self.wait(afterFailures: failures)
    }

    public static func wait(afterFailures n: Int) -> TimeInterval {
        guard n > 0 else { return 0 }
        return min(base * pow(2, Double(min(n, 16) - 1)), cap)
    }

    public mutating func noteFailure(key: String, now: Date) {
        if key != self.key {
            self.key = key
            failures = 0
        }
        failures += 1
        lastFailureAt = now
    }

    public mutating func reset() {
        key = nil
        failures = 0
        lastFailureAt = nil
    }
}
