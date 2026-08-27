package dataset

import (
	"crypto/sha256"
	"fmt"
)

// Version 返回 dataset_version：把所有能改变数据内容的输入折叠成一个短摘要。
//
// 覆盖 seed、行数、建表结构、行生成算法、分布参数。
// 少覆盖任何一项，都会出现「两份 dataset_version 相同但数据不同」的情况 ——
// 那意味着报告里的复现信息是假的，别人照着同一个 version 复现不出同一份数据。
func Version(seed uint64, rows int64) string {
	h := sha256.New()
	fmt.Fprintf(h, "seed=%d\nrows=%d\nschema=%s\ngenerator=%s\ndistribution=%s\n",
		seed, rows, SchemaVersion, GeneratorVersion, DistributionVersion)
	return fmt.Sprintf("%x", h.Sum(nil))[:12]
}
