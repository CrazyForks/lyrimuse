import QuartzCore

/// 菜单栏上的动画常驻不停(在放歌就一直动),两件事在这里统一管:
///
/// - **帧率上限**:不设 `preferredFrameRateRange` 时合成器按屏幕原生刷新率插值,ProMotion 屏上就是 120Hz ——
///   而这些东西一个 15pt 高的图标、一行十几 pt 的字,根本用不着。别的展示面都封到 30Hz
///   (`ProgressFillLayer` / `EqualizerBars` / `LyricsGapDots` / `LayerBreathing`),菜单栏按内容分三档。
/// - **冻结 / 恢复**:看不见的时候(锁屏、熄屏、切到别的用户、状态项所在的窗口不可见)把图层时间停住
///   (`speed = 0`),合成器就不用再逐帧重画;恢复时从停住的那一帧接着走。跟播放进度绑定的那几条
///   (逐字染色、进度图标)恢复后还要由调用方强制对一次表,冻结期间它们落后了多久就差多久。
enum MenuBarAnimation {
    /// 文字横移:30Hz 在一行字上能看出一顿一顿,给到 60。
    static let scrollFPS: Float = 60
    /// 逐字染色、间奏三点、图标律动:跟其它展示面同一档。
    static let decorativeFPS: Float = 30
    /// 进度图标:一首四分钟的歌在十几 pt 高的图标上约 8 秒才走 1pt,10Hz 绰绰有余。
    static let progressFPS: Float = 10

    @discardableResult
    static func capped<A: CAAnimation>(_ animation: A, fps: Float) -> A {
        animation.preferredFrameRateRange = CAFrameRateRange(minimum: min(5, fps), maximum: fps, preferred: fps)
        return animation
    }
}

extension CALayer {
    /// 把这一棵子树上的动画冻在当前帧。重复调用无副作用。
    func pauseMenuBarAnimations() {
        guard speed != 0 else { return }
        let now = convertTime(CACurrentMediaTime(), from: nil)
        speed = 0
        timeOffset = now
    }

    /// 从冻住的那一帧接着走。没冻过就什么都不做。
    func resumeMenuBarAnimations() {
        guard speed == 0 else { return }
        let pausedAt = timeOffset
        speed = 1
        timeOffset = 0
        beginTime = 0
        beginTime = convertTime(CACurrentMediaTime(), from: nil) - pausedAt
    }
}
