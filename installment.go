package credit

import "math"

// penaltySlot 记录一次计罚事件在买家事件树中的位置与当前金额。
type penaltySlot struct {
	seq    int64
	amount int64 // 当前事件金额；0 表示事件不存在
}

// installment 是一期分期的内部状态。
type installment struct {
	id        uint64
	buyer     uint64
	effective int64
	due       int64
	principal int64

	pays *evtTreap // 未撤销还款：分量 0 为金额（正）

	penSeqs [PenaltyMaxTimes]penaltySlot // 各计罚时刻对应的事件
	flagSeq int64                        // 逾期标记起始事件（+1）的 seq
	flagOn  bool                         // 起始事件当前是否存在
	endSeq  int64                        // 逾期标记结束事件（-1）的 seq
	endOn   bool                         // 结束事件当前是否存在
	clearAt int64                        // 本金结清时刻；math.MaxInt64 表示未结清
	prinSeq int64                        // 登记本金事件的 seq
}

// roundDiv 计算 v*num/den 四舍五入（half up），v 非负。
func roundDiv(v, num, den int64) int64 { return (v*num + den/2) / den }

// penaltyMoments 返回 8 个计罚时刻（到期后第 7、14、…、56 天）。
func (in *installment) penaltyMoments() [PenaltyMaxTimes]int64 {
	var m [PenaltyMaxTimes]int64
	for i := range m {
		m[i] = in.due + int64(i+1)*PenaltyIntervalDays*SecondsPerDay
	}
	return m
}

// overdueStart 返回该分期开始计入逾期标记的时刻。
func (in *installment) overdueStart() int64 {
	s := in.due + OverdueDays*SecondsPerDay
	if in.effective > s {
		s = in.effective
	}
	return s
}

// penaltyCap 返回累计罚金上限：原本金的 12% 四舍五入到分。
func (in *installment) penaltyCap() int64 {
	return roundDiv(in.principal, PenaltyCapNum, PenaltyCapDen)
}

// recompute 在还款变化后重算罚金事件与结清时刻，并把差额同步到买家事件树。
func (l *Ledger) recompute(in *installment) {
	bs := l.buyers[in.buyer]
	moments := in.penaltyMoments()
	capAmt := in.penaltyCap()
	var cum int64
	for i, m := range moments {
		paid := in.pays.prefix(m)[0]
		raw := roundDiv(in.principal-paid, PenaltyRateNum, PenaltyRateDen)
		if cum+raw > capAmt {
			raw = capAmt - cum
		}
		cum += raw
		slot := &in.penSeqs[i]
		if slot.amount == raw {
			continue
		}
		if slot.amount != 0 {
			bs.events.remove(m, slot.seq)
		}
		// 尚未生效的分期不产生罚金事件。
		if raw != 0 && m >= in.effective {
			bs.events.add(m, slot.seq, [4]int64{0, 0, raw, 0})
		}
		slot.amount = raw
	}

	oldClear := in.clearAt
	if in.pays.root != nil && in.pays.root.sum[0] >= in.principal {
		k, _ := in.pays.firstKeyGE(0, in.principal)
		in.clearAt = k
	} else {
		in.clearAt = math.MaxInt64
	}
	if in.clearAt == oldClear {
		return
	}

	s := in.overdueStart()
	oldOn := oldClear > s
	newOn := in.clearAt > s
	if in.endOn {
		bs.events.remove(oldClear, in.endSeq)
		in.endOn = false
	}
	if oldOn != newOn {
		if newOn {
			bs.events.add(s, in.flagSeq, [4]int64{0, 0, 0, 1})
		} else {
			bs.events.remove(s, in.flagSeq)
		}
		in.flagOn = newOn
	}
	if newOn && in.clearAt != math.MaxInt64 {
		bs.events.add(in.clearAt, in.endSeq, [4]int64{0, 0, 0, -1})
		in.endOn = true
	}
}
