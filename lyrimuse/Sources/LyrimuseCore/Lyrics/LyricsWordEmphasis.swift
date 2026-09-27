import Foundation

/// 拖长的词的强调效果(放大 + 辉光 + 额外上浮),参数照 Apple Music「正在播放」界面实测,
/// 见 07 章决策 61。只作用于拉丁字母这类词:中日韩文字的长音不做(Apple 也不做)。
///
/// 一个「词」是连续没有空白隔开的逐字 token 拼起来的(英文按音节拆成几个 token 时要合回一个词);
/// 中日韩文字每个 token 自成一个词,反正也不会触发。
public enum LyricsWordEmphasis {
    /// 触发门槛:这个词从开唱到唱完不短于这么多毫秒。
    public static let minDurationMs = 1000
    /// 词的字母数范围。太长的词整体放大会很突兀。
    public static let letterRange = 2...7

    /// 一个会被强调的词:从第一个 token 开唱到最后一个 token 唱完。
    public struct Span: Equatable, Sendable {
        public let startMs: Int
        public let endMs: Int

        public init(startMs: Int, endMs: Int) {
            self.startMs = startMs
            self.endMs = endMs
        }

        public var durationMs: Int { max(1, endMs - startMs) }
    }

    /// 某一刻的强调量。`scale` 是整词缩放倍数(1 = 不放大);`glow` 是辉光不透明度 0…1;
    /// `extraLift` 是在普通上浮之外再抬起的比例(0…1,乘上普通上浮的幅度)。
    public struct Frame: Equatable, Sendable {
        public let scale: Double
        public let glow: Double
        public let extraLift: Double

        public static let none = Frame(scale: 1, glow: 0, extraLift: 0)
    }

    /// 给一行的逐字 token 逐个标出所属的强调词(不强调的是 nil),下标与 `words` 一一对应。
    public static func spans(for words: [SyncedLyricWord]) -> [Span?] {
        var out = [Span?](repeating: nil, count: words.count)
        var chunk: [Int] = []
        func flush() {
            defer { chunk.removeAll() }
            guard let first = chunk.first, let last = chunk.last else { return }
            let text = chunk.map { words[$0].text }.joined()
            let span = Span(startMs: words[first].startMs,
                            endMs: words[last].startMs + max(1, words[last].durationMs))
            guard isEligible(text: text, durationMs: span.endMs - span.startMs) else { return }
            for i in chunk { out[i] = span }
        }
        for (i, w) in words.enumerated() {
            if containsCJK(w.text) {
                flush()
                continue
            }
            chunk.append(i)
            if let lastChar = w.text.last, lastChar.isWhitespace { flush() }
        }
        flush()
        return out
    }

    /// 这个词够不够格:时长够长、没有中日韩文字、字母数在 `letterRange` 内。
    public static func isEligible(text: String, durationMs: Int) -> Bool {
        guard durationMs >= minDurationMs, !containsCJK(text) else { return false }
        let letters = text.unicodeScalars.filter { CharacterSet.letters.contains($0) }.count
        return letterRange.contains(letters)
    }

    /// 某一刻的强调量(毫秒,跟逐字填色同一个时间基准)。三样都在词唱完的那一刻回到零:
    /// 句尾的词唱完就换行,这一行随即按非当前行渲染,留到词后面的效果会被硬切掉。
    ///
    /// * 放大:前 55% 升到峰值,70% 之后回落;峰值随时长变大(1.75 秒约 +4%、4 秒约 +6.5%,封顶 7%)。
    /// * 辉光:越唱越亮,80% 处最亮,最后 15% 淡掉。
    /// * 额外上浮:`sin(π·进度)`,词的一半处最高,唱完回到普通上浮的高度。
    public static func frame(for span: Span, atMs ms: Int) -> Frame {
        guard ms > span.startMs, ms < span.endMs else { return .none }
        let duration = Double(span.durationMs)
        let x = Double(ms - span.startMs) / duration
        let peak = min(0.07, 0.02 + 0.011 * duration / 1000)
        return Frame(
            scale: 1 + peak * smoothstep(0, 0.55, x) * (1 - smoothstep(0.7, 1, x)),
            glow: 0.55 * smoothstep(0.05, 0.8, x) * (1 - smoothstep(0.85, 1, x)),
            extraLift: sin(.pi * x))
    }

    static func containsCJK(_ s: String) -> Bool {
        s.unicodeScalars.contains { u in
            switch u.value {
            case 0x3040...0x30FF, 0x3400...0x4DBF, 0x4E00...0x9FFF, 0xF900...0xFAFF,
                 0xAC00...0xD7AF, 0x1100...0x11FF, 0x3130...0x318F, 0x20000...0x2FA1F:
                return true
            default:
                return false
            }
        }
    }

    private static func smoothstep(_ a: Double, _ b: Double, _ v: Double) -> Double {
        let k = min(1, max(0, (v - a) / (b - a)))
        return k * k * (3 - 2 * k)
    }
}
