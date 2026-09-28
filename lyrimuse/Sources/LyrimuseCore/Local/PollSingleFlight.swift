import Foundation

/// 播放轮询的单飞状态机:同一时刻只有一轮在飞,在飞时来的请求合并成「回来后补跑一次」。纯值类型,selftest 覆盖。
///
/// 为什么要它:轮询的触发源很多(2 秒定时器、通知去抖、四个探针回调、内容采纳),每轮都要同步等子进程。上一轮没回来
/// 就再起一轮的话,播放器卡住时(自动化授权弹窗挂着、每轮都等满超时)在飞的轮次一直涨,把协作线程池占满;几轮同时跑
/// 还会互相覆盖 `MediaControlClient` 的全局失败原因,按错宽限档清掉正在放的歌。合并成补跑不丢信号:最后一次请求之后
/// 一定还有一轮真的去读了。
public struct PollSingleFlight: Equatable, Sendable {
    public private(set) var inFlight = false
    public private(set) var rerunRequested = false

    public init() {}

    /// 要起一轮。true = 可以起;false = 已有一轮在飞,记下补跑。
    public mutating func begin() -> Bool {
        if inFlight {
            rerunRequested = true
            return false
        }
        inFlight = true
        return true
    }

    /// 在飞的那一轮作废了(拖动进度条:它的快照是拖动之前抓的),回来后要补跑一轮拿新状态。
    public mutating func invalidateInFlight() {
        if inFlight { rerunRequested = true }
    }

    /// 一轮收尾。true = 期间有人要过,立刻补跑一轮。
    public mutating func finish() -> Bool {
        inFlight = false
        let rerun = rerunRequested
        rerunRequested = false
        return rerun
    }
}
