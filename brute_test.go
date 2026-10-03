package ledger

import "sort"

// bruteForce 是测试专用的独立参照实现：不建任何索引，
// 每次查询都依据最新事实从头逐笔计算，用于与 Ledger 的增量索引结果对拍。
type bruteForce struct {
	installments map[int64]Installment
	repayments   map[int64]Repayment // 仅未撤销
}

func newBruteForce() *bruteForce {
	return &bruteForce{
		installments: make(map[int64]Installment),
		repayments:   make(map[int64]Repayment),
	}
}

func (bf *bruteForce) register(inst Installment) {
	bf.installments[inst.ID] = inst
}

func (bf *bruteForce) put(rep Repayment) {
	if old, ok := bf.repayments[rep.ID]; ok {
		// 更正不能改变所属分期
		rep.InstallmentID = old.InstallmentID
	}
	bf.repayments[rep.ID] = rep
}

func (bf *bruteForce) revoke(id int64) {
	delete(bf.repayments, id)
}

func roundHalfUp15permille(cents int64) int64 { return (cents*15 + 500) / 1000 }

func penaltyCap(principal int64) int64 { return (principal*12 + 50) / 100 }

func (bf *bruteForce) summary(buyer int64, at int64) Summary {
	var out Summary
	for _, inst := range bf.installments {
		if inst.Buyer != buyer || inst.Effective > at {
			continue
		}
		var reps []Repayment
		for _, r := range bf.repayments {
			if r.InstallmentID == inst.ID {
				reps = append(reps, r)
			}
		}
		sort.Slice(reps, func(i, j int) bool { return reps[i].Time < reps[j].Time })
		repaidAt := func(tm int64) int64 {
			var s int64
			for _, r := range reps {
				if r.Time <= tm {
					s += r.Amount
				}
			}
			return s
		}
		outstanding := inst.Principal - repaidAt(at)
		out.OutstandingPrincipal += outstanding

		cap := penaltyCap(inst.Principal)
		var cum int64
		for j := 1; j <= 8; j++ {
			p := inst.Due + int64(j)*7*daySeconds
			if p < inst.Effective {
				p = inst.Effective
			}
			if p > at {
				break
			}
			raw := roundHalfUp15permille(inst.Principal - repaidAt(p))
			cum += raw
			if cum > cap {
				cum = cap
			}
		}
		out.AccruedPenalty += cum

		start := inst.Due + 15*daySeconds
		if inst.Effective > start {
			start = inst.Effective
		}
		if outstanding > 0 && at >= start {
			out.HasOverdue15 = true
		}
	}
	return out
}
