package ledger

import "math"

// Summary 依据最新更正后的事实，返回买家在 at 时刻（UTC 秒，含该时刻）的历史汇总。
// 尚未生效的分期不计入；同秒发生的还款先于计罚生效。
func (l *Ledger) Summary(buyer int64, at int64) Summary {
	b := l.buyers[buyer]
	if b == nil {
		return Summary{}
	}
	upper := treapKey{at, math.MaxInt64}
	principal := b.principals.prefixSum(upper)
	repaid := b.repays.prefixSum(upper)
	penalty := b.penalties.prefixSum(upper)
	// 存在某分期：逾期观察起点 <= at 且结清时刻 > at（即本金在 at 时刻未清）。
	tau := b.overdue.prefixMax(upper)
	return Summary{
		OutstandingPrincipal: principal - repaid,
		AccruedPenalty:       penalty,
		HasOverdue15:         tau > at,
	}
}
