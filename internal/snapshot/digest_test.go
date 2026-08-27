package snapshot

import (
	"database/sql"
	"testing"
	"time"
)

// 本文件覆盖的风险：摘要必须能发现两侧任何一处数据差异。
// 摘要漏判 = 拿着不可比的数据出性能结论，比不出结论更危险。

func baseRow() Row {
	return Row{
		ID:          1,
		UserID:      42,
		Status:      "paid",
		CreatedAt:   time.Date(2024, 1, 1, 0, 0, 0, 123456000, time.UTC),
		AmountCents: 1770274,
		Note:        sql.NullString{String: "note-7511367e", Valid: true},
		Payload:     "54c7e2fb8a2fc3edcab861750981195d",
	}
}

func digestOf(t *testing.T, rows ...Row) string {
	t.Helper()
	h := NewHasher()
	for _, r := range rows {
		h.Add(r)
	}
	d, n := h.Sum()
	if n != int64(len(rows)) {
		t.Fatalf("行数计数错误: 期望 %d 实际 %d", len(rows), n)
	}
	return d
}

func TestDigestIsDeterministic(t *testing.T) {
	r := baseRow()
	if a, b := digestOf(t, r), digestOf(t, r); a != b {
		t.Fatalf("同一行两次摘要不同: %s != %s", a, b)
	}
}

// 逐字段改一处，摘要都必须变。这是「两侧各自造数」这一简化的核心保险。
func TestDigestChangesOnAnyFieldChange(t *testing.T) {
	original := digestOf(t, baseRow())

	cases := map[string]func(*Row){
		"id":           func(r *Row) { r.ID = 2 },
		"user_id":      func(r *Row) { r.UserID = 43 },
		"status":       func(r *Row) { r.Status = "pending" },
		"created_at":   func(r *Row) { r.CreatedAt = r.CreatedAt.Add(time.Microsecond) },
		"amount_cents": func(r *Row) { r.AmountCents++ },
		"note":         func(r *Row) { r.Note.String = "note-00000000" },
		"payload":      func(r *Row) { r.Payload = "00000000000000000000000000000000" },
	}

	for field, mutate := range cases {
		t.Run(field, func(t *testing.T) {
			r := baseRow()
			mutate(&r)
			if got := digestOf(t, r); got == original {
				t.Fatalf("改动 %s 后摘要未变，该字段没有真正参与摘要", field)
			}
		})
	}
}

// created_at 的微秒必须完整参与。DATETIME(6) 被截断到秒会让两侧的细微差异隐身。
func TestDigestKeepsMicrosecondPrecision(t *testing.T) {
	a := baseRow()
	b := baseRow()
	b.CreatedAt = b.CreatedAt.Add(time.Microsecond)
	if digestOf(t, a) == digestOf(t, b) {
		t.Fatal("相差 1 微秒的 created_at 摘要相同，时间精度被截断了")
	}
}

// NULL 与空字符串是不同的数据，摘要必须区分。
func TestDigestDistinguishesNullFromEmptyString(t *testing.T) {
	null := baseRow()
	null.Note = sql.NullString{Valid: false}

	empty := baseRow()
	empty.Note = sql.NullString{String: "", Valid: true}

	if digestOf(t, null) == digestOf(t, empty) {
		t.Fatal("note IS NULL 与 note='' 摘要相同，两者会互相冒充")
	}
}

// 摘要按主键升序累加，因此行序参与摘要；顺序错乱必须被发现。
func TestDigestIsOrderSensitive(t *testing.T) {
	r1 := baseRow()
	r2 := baseRow()
	r2.ID = 2

	if digestOf(t, r1, r2) == digestOf(t, r2, r1) {
		t.Fatal("交换行序后摘要相同，行序未参与摘要")
	}
}

// 字段值里若含分隔符，不得借此伪造出另一种字段切分。
func TestDigestResistsSeparatorInjection(t *testing.T) {
	a := baseRow()
	a.Status = "paid"
	a.Note = sql.NullString{String: "x", Valid: true}

	b := baseRow()
	// 把分隔符塞进 status，试图让 (status, ..., note) 与 a 拼出同样的字节流
	b.Status = "paid" + string(rune(fieldSep)) + "x"

	if digestOf(t, a) == digestOf(t, b) {
		t.Fatal("字段中的分隔符可以伪造字段边界，规范化不安全")
	}
}

func TestEmptyTableDigestIsStable(t *testing.T) {
	a, na := NewHasher().Sum()
	b, nb := NewHasher().Sum()
	if a != b || na != 0 || nb != 0 {
		t.Fatalf("空表摘要不稳定: %s/%d vs %s/%d", a, na, b, nb)
	}
}
