package ledger

import "math"

// PutRepayment 写入一笔还款；若还款 ID 已存在则按最新金额与生效时刻更正。
// 更正不能改变所属分期，传入的 InstallmentID 与原有记录不一致时以原记录为准。
func (l *Ledger) PutRepayment(rep Repayment) {
	if r, ok := l.repayments[rep.ID]; ok {
		it := r.inst
		b := l.buyers[it.buyer]
		oldKey := treapKey{r.time, int64(r.idx)}
		it.reps.erase(oldKey)
		b.repays.erase(oldKey)
		it.totalRepaid -= r.amount
		r.amount = rep.Amount
		r.time = rep.Time
		newKey := treapKey{r.time, int64(r.idx)}
		it.reps.insert(newKey, r.amount)
		b.repays.insert(newKey, r.amount)
		it.totalRepaid += r.amount
		l.refreshInstallment(it, b)
		return
	}
	it, ok := l.installments[rep.InstallmentID]
	if !ok {
		return
	}
	b := l.buyers[it.buyer]
	r := &repayment{
		id:     rep.ID,
		inst:   it,
		amount: rep.Amount,
		time:   rep.Time,
		idx:    l.nextRepIdx,
	}
	l.nextRepIdx++
	l.repayments[rep.ID] = r
	key := treapKey{r.time, int64(r.idx)}
	it.reps.insert(key, r.amount)
	b.repays.insert(key, r.amount)
	it.totalRepaid += r.amount
	l.refreshInstallment(it, b)
}

// RevokeRepayment 撤销一笔还款，视为该笔还款从未发生。ID 不存在时为空操作。
func (l *Ledger) RevokeRepayment(repaymentID int64) {
	r, ok := l.repayments[repaymentID]
	if !ok {
		return
	}
	delete(l.repayments, repaymentID)
	it := r.inst
	b := l.buyers[it.buyer]
	key := treapKey{r.time, int64(r.idx)}
	it.reps.erase(key)
	b.repays.erase(key)
	it.totalRepaid -= r.amount
	l.refreshInstallment(it, b)
}

// refreshInstallment 在分期还款集合变化后，重算其计罚事件与结清时刻，
// 并同步到买家级聚合索引。
func (l *Ledger) refreshInstallment(it *installment, b *buyerState) {
	// 1. 从买家罚金事件树移除旧的计罚事件。
	for k := 0; k < it.penCount; k++ {
		ev := it.penEvents[k]
		b.penalties.erase(treapKey{ev.time, it.penKey(k)})
	}
	it.penCount = 0

	// 2. 依据最新还款事实重算 8 个计罚时刻的增量罚金（含封顶）。
	cap := (it.principal*12 + 50) / 100 // 原本金的 12%，四舍五入到分
	var cum int64
	for j := 1; j <= penaltyTimes; j++ {
		p := it.due + int64(j)*penaltyInterval
		if p < it.effective {
			// 分期尚未生效时不计罚，顺延至生效时刻。
			p = it.effective
		}
		outstanding := it.principal - it.reps.prefixSum(treapKey{p, math.MaxInt64})
		raw := (outstanding*15 + 500) / 1000 // 未偿本金的 1.5%，四舍五入到分
		next := cum + raw
		if next > cap {
			next = cap // 触及上限的那笔只收剩余额度
		}
		if delta := next - cum; delta > 0 {
			ev := penEvent{time: p, delta: delta}
			it.penEvents[it.penCount] = ev
			b.penalties.insert(treapKey{ev.time, it.penKey(it.penCount)}, ev.delta)
			it.penCount++
		}
		cum = next
	}

	// 3. 重算结清时刻 tau，并同步买家逾期索引。
	tau := int64(math.MaxInt64)
	switch {
	case it.principal <= 0:
		tau = math.MinInt64
	case it.totalRepaid >= it.principal:
		tau, _ = it.reps.timeAtPrefix(it.principal)
	}
	if tau != it.tau {
		key := treapKey{it.start, int64(it.idx)}
		b.overdue.erase(key)
		it.tau = tau
		b.overdue.insert(key, tau)
	}
}

// penKey 为第 k 个计罚事件生成买家罚金事件树键的第二维，保证全局唯一。
func (it *installment) penKey(k int) int64 {
	return int64(it.idx)*penaltyTimes + int64(k)
}
