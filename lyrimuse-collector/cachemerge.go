package main

import (
	"encoding/json"
	"os"
)

// mergeMissingFromDisk 存盘前把盘上那份里、这份没有的条目并进 keep(只收 keep 函数认可的非空值)。
//
// 几份按歌手缓存的查询结果(MusicBrainz 别名 / 主名、QQ 歌手名)同时有两个写入方:常驻 collector,和
// 「搜索候选歌词」弹窗起的 search-lyrics 子进程(它一开始就在后台预热别名,见 searchcli.go)。两边都是启动时读一次、
// 存盘时整份写回,子进程跑的那二十来秒里常驻进程新学到的条目会被它盖掉。存盘前先并一次盘上的,谁写都不丢对方的。
// 同一个 key 两边都有时以这份为准。盘上的读不出、解不开就当没有。
func mergeMissingFromDisk[V any](path string, keep map[string]V, usable func(V) bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var disk map[string]V
	if json.Unmarshal(data, &disk) != nil {
		return
	}
	for k, v := range disk {
		if _, ok := keep[k]; !ok && usable(v) {
			keep[k] = v
		}
	}
}
