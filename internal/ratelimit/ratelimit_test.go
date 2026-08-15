// ai-form-backend - AGPL-3.0
package ratelimit

import (
	"testing"
	"time"
)

// 多条规则整体判定:被后一条拒绝时,前面几条的配额不能被吃掉
func TestAllowAllNoPartialConsume(t *testing.T) {
	l := New()
	wide := Rule{Key: "wide", Limit: 5, Window: time.Minute}
	narrow := Rule{Key: "narrow", Limit: 1, Window: time.Minute}

	if denied := l.AllowAll(wide, narrow); denied != "" {
		t.Fatalf("首次应放行,实际被 %q 拒绝", denied)
	}
	// 第二次被 narrow 拒:wide 不该因此被计数
	if denied := l.AllowAll(wide, narrow); denied != "narrow" {
		t.Fatalf("第二次应被 narrow 拒绝,实际 %q", denied)
	}
	if denied := l.AllowAll(wide, narrow); denied != "narrow" {
		t.Fatalf("第三次应仍被 narrow 拒绝,实际 %q", denied)
	}
	// wide 只在首次成功时被计过 1 次,应还剩 4 次
	for i := 0; i < 4; i++ {
		if !l.Allow("wide", 5, time.Minute) {
			t.Fatalf("wide 第 %d 次应仍有余量(被拒请求不该消耗它的配额)", i+2)
		}
	}
	if l.Allow("wide", 5, time.Minute) {
		t.Fatal("wide 用满 5 次后应拒绝")
	}
}

// 单条规则:窗口内计数,超限拒绝,换窗重置
func TestAllowWindow(t *testing.T) {
	l := New()
	for i := 0; i < 3; i++ {
		if !l.Allow("k", 3, 50*time.Millisecond) {
			t.Fatalf("第 %d 次应放行", i+1)
		}
	}
	if l.Allow("k", 3, 50*time.Millisecond) {
		t.Fatal("第 4 次应拒绝")
	}
	time.Sleep(60 * time.Millisecond)
	if !l.Allow("k", 3, 50*time.Millisecond) {
		t.Fatal("换窗后应重新放行")
	}
}
