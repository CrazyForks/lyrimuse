import AppKit
import QuartzCore
import SwiftUI

/// 把一块**静态**的 SwiftUI 内容交给 Core Animation 做上下往复漂浮。
///
/// 跟 `LayerBreathing` 同一个思路:内容只画一次(`NSHostingView`),漂浮挂在宿主图层的 `sublayerTransform`
/// 上,装好之后由渲染服务播,主线程不参与。别换回 `TimelineView(.animation)` 逐帧算偏移:那样页面开着
/// 就每帧重排一遍整棵视图树(关于页页头实测约 11% CPU,见 14 章决策 43)。
///
/// `phase` 是弧度,换算成动画的起始进度,让几个符号不同步起落。`animating` = false 时摘掉动画、停在基准位。
struct LayerFloating<Content: View>: NSViewRepresentable {
    /// 偏离基准位的最大距离(点),上下对称。
    var amplitude: CGFloat
    /// 往返一次的秒数。
    var period: Double
    /// 起始相位(弧度),同 `sin(t * 2π / period + phase)` 里的那个 phase。
    var phase: Double = 0
    var animating: Bool
    @ViewBuilder var content: Content

    func makeNSView(context: Context) -> LayerFloatingNSView {
        LayerFloatingNSView(rootView: AnyView(content))
    }

    func updateNSView(_ view: LayerFloatingNSView, context: Context) {
        view.hosting.rootView = AnyView(content)
        view.configure(amplitude: amplitude, period: period, phase: phase, animating: animating)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, nsView: LayerFloatingNSView, context: Context) -> CGSize? {
        nsView.hosting.fittingSize
    }
}

@MainActor
final class LayerFloatingNSView: NSView {
    private static let floatKey = "lyrimuse.floating-offset"

    let hosting: NSHostingView<AnyView>
    private var amplitude: CGFloat = 0
    private var period: Double = 1
    private var phase: Double = 0
    private var animating = false

    init(rootView: AnyView) {
        hosting = NSHostingView(rootView: rootView)
        super.init(frame: .zero)
        wantsLayer = true
        hosting.translatesAutoresizingMaskIntoConstraints = true
        hosting.autoresizingMask = [.width, .height]
        addSubview(hosting)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("not used") }

    func configure(amplitude: CGFloat, period: Double, phase: Double, animating: Bool) {
        let changed = self.amplitude != amplitude || self.period != period || self.phase != phase
            || self.animating != animating
        self.amplitude = amplitude
        self.period = period
        self.phase = phase
        self.animating = animating
        if changed { reinstall() }
    }

    override func layout() {
        super.layout()
        hosting.frame = bounds
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        reinstall()
    }

    private func reinstall() {
        guard let layer else { return }
        layer.removeAnimation(forKey: Self.floatKey)
        guard animating, window != nil, amplitude > 0, period > 0 else { return }
        // 单程(从最低到最高)是半个周期;easeInEaseOut 往复近似正弦。AppKit 视图坐标 y 向上,
        // 正弦的 +1 对应 SwiftUI 里往下,所以从 +amplitude 走到 -amplitude。
        let move = CABasicAnimation(keyPath: "sublayerTransform")
        move.fromValue = NSValue(caTransform3D: CATransform3DMakeTranslation(0, amplitude, 0))
        move.toValue = NSValue(caTransform3D: CATransform3DMakeTranslation(0, -amplitude, 0))
        move.duration = period / 2
        move.autoreverses = true
        move.repeatCount = .infinity
        move.timingFunction = CAMediaTimingFunction(name: .easeInEaseOut)
        // sin 的相位换算成起始进度:sin 在 -π/2 处最低,从那里算起走过的比例 × 一个周期。
        let fraction = ((phase + .pi / 2).truncatingRemainder(dividingBy: 2 * .pi) + 2 * .pi)
            .truncatingRemainder(dividingBy: 2 * .pi) / (2 * .pi)
        move.timeOffset = fraction * period
        // 几秒一个来回的慢漂,合成器不必按 ProMotion 120Hz 去插值。
        move.preferredFrameRateRange = CAFrameRateRange(minimum: 10, maximum: 30, preferred: 30)
        layer.add(move, forKey: Self.floatKey)
    }
}
