import Foundation

/// 「收听时段」卡:把「每天每个钟点几次」按范围汇总成一天 24 小时、一周 7 天两组分布。
///
/// 输入是日桶的同构物(`"yyyy-MM-dd"` → 24 个整数,本地时区,时间取 scrobble 的开播时刻),
/// 跟热力图日桶同一次历史扫描写入,所以范围按日桶键截,不按时间戳。
public enum ListeningHours {
    /// 汇总范围。「近 30 天」「近一年」含今天,跟榜单同名时段的叫法一致。
    public enum Span: String, CaseIterable, Hashable {
        case month, year, overall

        /// 往回数几天(含今天);nil = 不限。
        var days: Int? {
            switch self {
            case .month: return 30
            case .year: return 365
            case .overall: return nil
            }
        }
    }

    public struct Summary: Equatable {
        /// 下标 0…23 = 0 点…23 点。
        public let hours: [Int]
        /// 下标 0…6 = 周一…周日。
        public let weekdays: [Int]
        public let total: Int
        public let peakHour: Int
        public let quietestHour: Int
        /// 0 = 周一。
        public let peakWeekday: Int

        public init(hours: [Int], weekdays: [Int], total: Int, peakHour: Int, quietestHour: Int, peakWeekday: Int) {
            self.hours = hours
            self.weekdays = weekdays
            self.total = total
            self.peakHour = peakHour
            self.quietestHour = quietestHour
            self.peakWeekday = peakWeekday
        }
    }

    /// 每个钟点一格的数组长度。存盘的每天那一行必须正好这么长,长度不对的行整行忽略。
    public static let hoursPerDay = 24

    /// 范围内一次都没有时返回 nil。并列时取最早的钟点 / 最靠前的星期几,结果稳定。
    public static func summarize(
        hourly: [String: [Int]], span: Span, today: Date,
        calendar: Calendar = .current, dayKey: (Date) -> String
    ) -> Summary? {
        let cutoff: String? = span.days.flatMap { days in
            calendar.date(byAdding: .day, value: -(days - 1), to: calendar.startOfDay(for: today)).map(dayKey)
        }
        var hours = [Int](repeating: 0, count: hoursPerDay)
        var weekdays = [Int](repeating: 0, count: 7)
        for (key, row) in hourly where row.count == hoursPerDay {
            // "yyyy-MM-dd" 字典序即时间序。
            if let cutoff, key < cutoff { continue }
            guard let day = ListeningMilestones.parseDay(key, calendar: calendar) else { continue }
            // Calendar 的 weekday 1 = 周日;换成周一在前。
            let wd = (calendar.component(.weekday, from: day) + 5) % 7
            for h in 0..<hoursPerDay {
                hours[h] += row[h]
                weekdays[wd] += row[h]
            }
        }
        let total = hours.reduce(0, +)
        guard total > 0 else { return nil }
        func argmax(_ a: [Int]) -> Int { a.indices.max { a[$0] < a[$1] || (a[$0] == a[$1] && $0 > $1) }! }
        let quietest = hours.indices.min { hours[$0] < hours[$1] || (hours[$0] == hours[$1] && $0 < $1) }!
        return Summary(hours: hours, weekdays: weekdays, total: total,
                       peakHour: argmax(hours), quietestHour: quietest, peakWeekday: argmax(weekdays))
    }
}
