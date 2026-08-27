package snapshot

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"hash"
	"strconv"
	"time"
)

// 分隔符用 ASCII 控制符：orders 各列的取值（十进制数字、状态词、十六进制串、
// note-xxxx）都不可能含有它们，因此不同的字段切分不会拼出相同的字节流。
const (
	fieldSep = 0x1f // Unit Separator
	rowSep   = 0x1e // Record Separator

	noteNull    = 0x00
	notePresent = 0x01
)

// timeLayout 固定到微秒并显式带 Z。DATETIME(6) 的精度必须完整参与摘要，
// 否则两侧相差几微秒的数据会被判为一致。
const timeLayout = "2006-01-02T15:04:05.000000Z"

// Row 是参与摘要的一行。字段顺序即摘要中的字节顺序，调换会改变摘要。
type Row struct {
	ID          uint64
	UserID      uint64
	Status      string
	CreatedAt   time.Time
	AmountCents int64
	Note        sql.NullString
	Payload     string
}

// normalize 把一行规范化为确定性字节序列。
// 规范化的全部要点：定长时间格式、UTC、NULL 与空串用不同前缀区分、字段间加分隔符。
func normalize(dst []byte, r Row) []byte {
	dst = strconv.AppendUint(dst, r.ID, 10)
	dst = append(dst, fieldSep)
	dst = strconv.AppendUint(dst, r.UserID, 10)
	dst = append(dst, fieldSep)
	dst = append(dst, r.Status...)
	dst = append(dst, fieldSep)
	dst = r.CreatedAt.UTC().AppendFormat(dst, timeLayout)
	dst = append(dst, fieldSep)
	dst = strconv.AppendInt(dst, r.AmountCents, 10)
	dst = append(dst, fieldSep)
	// NULL 与空字符串必须产生不同字节，否则 note IS NULL 与 note='' 会互相冒充。
	if r.Note.Valid {
		dst = append(dst, notePresent)
		dst = append(dst, r.Note.String...)
	} else {
		dst = append(dst, noteNull)
	}
	dst = append(dst, fieldSep)
	dst = append(dst, r.Payload...)
	dst = append(dst, rowSep)
	return dst
}

// Hasher 增量计算摘要，不在内存里保留整表。
type Hasher struct {
	h    hash.Hash
	buf  []byte
	rows int64
}

func NewHasher() *Hasher {
	return &Hasher{h: sha256.New(), buf: make([]byte, 0, 512)}
}

// Add 按调用顺序把行喂进摘要。顺序参与摘要，因此调用方必须按主键升序调用。
func (h *Hasher) Add(r Row) {
	h.buf = normalize(h.buf[:0], r)
	h.h.Write(h.buf)
	h.rows++
}

// Sum 返回十六进制摘要与已计入的行数。
func (h *Hasher) Sum() (string, int64) {
	return hex.EncodeToString(h.h.Sum(nil)), h.rows
}
