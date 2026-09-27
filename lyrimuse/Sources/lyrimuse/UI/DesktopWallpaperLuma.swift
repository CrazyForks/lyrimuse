import AppKit
import ImageIO

/// 当前桌面壁纸的平均相对亮度。
///
/// 歌词窗口的自定义背景色带不透明度时,窗口本体是透明的,颜色后面透出来的是桌面 —— 文字该用白字还是深色字,
/// 得看颜色跟**壁纸**混出来有多亮,按窗口底色算会在「深色外观 + 浅色壁纸」下配出看不见的白字。
/// 读的是壁纸文件本身(`NSWorkspace.desktopImageURL`),不截屏、不需要屏幕录制权限;代价是盖在壁纸上的
/// 别的窗口算不进来。
///
/// 这个值在歌词窗口的 body 里每次重算都会被问好几遍:按壁纸地址缓存,每个地址最多 5 秒回盘上核对一次
/// 有没有换壁纸,其余时候只是一次字典查找。解码用 ImageIO 的缩略图,不整张解开几千像素的原图。
@MainActor
enum DesktopWallpaperLuma {
    private struct Entry {
        var modified: Date?
        var luma: Double?
        var checkedAt: Date
    }

    private static var cache: [URL: Entry] = [:]
    private static let recheckInterval: TimeInterval = 5

    /// nil = 读不到(拿不到屏幕、壁纸地址,或者文件解不开),调用方按系统外观估计。
    static func luma(for screen: NSScreen?) -> Double? {
        guard let screen = screen ?? NSScreen.main,
              let url = NSWorkspace.shared.desktopImageURL(for: screen) else { return nil }
        let now = Date()
        if let entry = cache[url], now.timeIntervalSince(entry.checkedAt) < recheckInterval {
            return entry.luma
        }
        let modified = (try? url.resourceValues(forKeys: [.contentModificationDateKey]))?.contentModificationDate
        if var entry = cache[url], entry.modified == modified {
            entry.checkedAt = now
            cache[url] = entry
            return entry.luma
        }
        let luma = averageLuma(of: url)
        cache[url] = Entry(modified: modified, luma: luma, checkedAt: now)
        return luma
    }

    /// 缩到 16×16 以内再按 sRGB 线性化后的相对亮度取平均。
    private static func averageLuma(of url: URL) -> Double? {
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceThumbnailMaxPixelSize: 16,
        ]
        guard let source = CGImageSourceCreateWithURL(url as CFURL, nil),
              let image = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else { return nil }
        let width = image.width, height = image.height
        guard width > 0, height > 0, let space = CGColorSpace(name: CGColorSpace.sRGB) else { return nil }
        var pixels = [UInt8](repeating: 0, count: width * height * 4)
        let drawn = pixels.withUnsafeMutableBytes { buffer -> Bool in
            guard let context = CGContext(data: buffer.baseAddress, width: width, height: height, bitsPerComponent: 8,
                                          bytesPerRow: width * 4, space: space,
                                          bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else { return false }
            context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
            return true
        }
        guard drawn else { return nil }
        func linear(_ v: UInt8) -> Double {
            let c = Double(v) / 255
            return c <= 0.04045 ? c / 12.92 : pow((c + 0.055) / 1.055, 2.4)
        }
        var total = 0.0
        for i in stride(from: 0, to: pixels.count, by: 4) {
            total += 0.2126 * linear(pixels[i]) + 0.7152 * linear(pixels[i + 1]) + 0.0722 * linear(pixels[i + 2])
        }
        return total / Double(width * height)
    }
}
