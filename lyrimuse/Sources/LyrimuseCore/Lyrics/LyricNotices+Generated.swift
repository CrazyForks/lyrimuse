// 由 scripts/gen-lyric-notices.py 从 shared/lyric-notices.json 生成,请勿手改。
// 改规则:改那份 JSON → 跑 `python3 scripts/gen-lyric-notices.py`。CI 跑 `--check` 对账,手改这里会红。

extension LyricNotices {
    static let rules: [Rule] = [
        Rule(id: "translation-copyright", pattern: "翻[译譯]作品的?著作[权權]", ignoreCase: false, translation: true, body: true),
        Rule(id: "qq-copyright", pattern: "QQ音乐.*著作[权權]|著作[权權].*QQ音乐", ignoreCase: false, translation: true, body: true),
        Rule(id: "translator-notice", pattern: "歌词翻译由.*提供|提供.*歌词翻译由", ignoreCase: false, translation: true, body: true),
        Rule(id: "unlicensed-use", pattern: "未经[^。]{0,12}(许可|授权|同意)", ignoreCase: true, translation: false, body: true),
        Rule(id: "forbidden-use", pattern: "不得(翻录|翻唱|复制|转载|使用|下载)", ignoreCase: true, translation: false, body: true),
        Rule(id: "rights-reserved-zh", pattern: "版权所有|保留(所有)?权利", ignoreCase: true, translation: false, body: true),
        Rule(id: "rights-reserved-en", pattern: "all rights reserved|unauthor(i[sz]ed)? (copying|reproduction|duplication)", ignoreCase: true, translation: false, body: true),
        Rule(id: "license-obtained", pattern: "(已获|已獲|获得|獲得|取得|经过|經過|通过|通過|获授|獲授)[^。]{0,10}授[权權]", ignoreCase: true, translation: false, body: true),
        Rule(id: "license-official", pattern: "(正版|正式|独家|獨家|官方)授[权權]", ignoreCase: true, translation: false, body: true),
        Rule(id: "subtitle-generated", pattern: "字幕由.{0,24}(技术|技術)生成", ignoreCase: true, translation: false, body: true),
        Rule(id: "company-provided", pattern: "^由.{1,30}(公司|Co\\.?,? ?Ltd\\.?)提供$", ignoreCase: true, translation: false, body: true),
        Rule(id: "sample-credit", pattern: "^contains (an? )?(interpolations?|samples?|elements?) (of|from)([^A-Za-z]|$)", ignoreCase: true, translation: false, body: true),
    ]
}
