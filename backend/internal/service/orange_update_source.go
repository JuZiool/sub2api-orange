// Orange 特有：更新源仓库与 Orange 版本号（x.y.z-N）规则。
//
// 上游 update_service 以 Wei-Shaw/sub2api 的 Release 为准，并把版本号当作 semver 处理：
// "-N" 被当成预发布标记直接截断丢弃。Orange 的版本号是「同一上游版本的第 N 次发布」
// （如 0.2.24-6 -> 0.2.24-7），这个 N 必须参与比较，否则 Orange 自己的版本更新永远不会被
// 识别；更新检查、一键更新与版本回退都以 Orange 仓库的 Release 为准。
//
// 上游文件里只保留两处接线：仓库地址常量引用 orangeUpdateRepo，compareVersions 在 x.y.z
// 三元组相等时把胜负交给 orangeCompareVersionRevision。没有 "-N" 后缀时两边都算第 0 次
// 发布，比较结果与上游完全一致。
package service

import (
	"strconv"
	"strings"
)

// orangeUpdateRepo 是 Orange 发行版仓库：更新检查、一键更新与版本回退都取它的 Release。
const orangeUpdateRepo = "JuZiool/sub2api-orange"

// orangeCompareVersionRevision 比较两个版本号 "-N" 后缀的大小，供 compareVersions 在
// x.y.z 三元组相等时定胜负。
//
// 解析口径与上游 parseVersion 保持一致：先去掉前导 "v"，再按第一个 '-' 切分后缀；
// 没有后缀或后缀不是纯数字（如 -beta、-rc1）都视为第 0 次发布；不做空白裁剪。
func orangeCompareVersionRevision(current, latest string) int {
	currentRevision := orangeVersionRevision(current)
	latestRevision := orangeVersionRevision(latest)
	switch {
	case currentRevision < latestRevision:
		return -1
	case currentRevision > latestRevision:
		return 1
	default:
		return 0
	}
}

// orangeVersionRevision 取版本号的 "-N" 后缀作为第几次发布，取不到时返回 0。
func orangeVersionRevision(version string) int {
	v := strings.TrimPrefix(version, "v")
	idx := strings.IndexByte(v, '-')
	if idx == -1 {
		return 0
	}
	revision, err := strconv.Atoi(v[idx+1:])
	if err != nil {
		return 0
	}
	return revision
}
