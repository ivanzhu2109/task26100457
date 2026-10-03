package ledger

import (
	"math/rand"
	"testing"
)

const testBase = int64(1_700_000_000)

// 还款跨过计罚时刻：同秒还款先于计罚；更正还款时间会改变历史计罚。
func TestRepaymentCrossesPenaltyMoment(t *testing.T) {
	newLedger := func() *Ledger {
		l := New()
		l.RegisterInstallment(Installment{ID: 1, Buyer: 10, Effective: testBase, Due: testBase, Principal: 1_000_000})
		return l
	}
	day7 := testBase + 7*daySeconds
	q := testBase + 60*daySeconds // 所有计罚结束之后查询

	// 计罚前一日结清：无罚金
	l := newLedger()
	l.PutRepayment(Repayment{ID: 100, InstallmentID: 1, Amount: 1_000_000, Time: day7 - daySeconds})
	if s := l.Summary(10, q); s.AccruedPenalty != 0 || s.OutstandingPrincipal != 0 {
		t.Fatalf("paid before first penalty: got %+v", s)
	}

	// 与计罚同秒结清：还款先生效，无罚金
	l = newLedger()
	l.PutRepayment(Repayment{ID: 100, InstallmentID: 1, Amount: 1_000_000, Time: day7})
	if s := l.Summary(10, q); s.AccruedPenalty != 0 {
		t.Fatalf("paid at the same second as penalty: got %+v", s)
	}

	// 计罚后一秒结清：第 7 天按全额本金计罚 15000 分
	l = newLedger()
	l.PutRepayment(Repayment{ID: 100, InstallmentID: 1, Amount: 1_000_000, Time: day7 + 1})
	if s := l.Summary(10, q); s.AccruedPenalty != 15000 {
		t.Fatalf("paid one second after penalty: got %+v", s)
	}

	// 更正还款时间使其跨过计罚时刻，历史罚金随之改变
	l = newLedger()
	l.PutRepayment(Repayment{ID: 100, InstallmentID: 1, Amount: 1_000_000, Time: day7 - 1})
	if s := l.Summary(10, q); s.AccruedPenalty != 0 {
		t.Fatalf("before correction: got %+v", s)
	}
	l.PutRepayment(Repayment{ID: 100, InstallmentID: 1, Amount: 1_000_000, Time: day7 + 1})
	if s := l.Summary(10, q); s.AccruedPenalty != 15000 {
		t.Fatalf("after moving repayment past penalty moment: got %+v", s)
	}
	// 查询更正之前的时刻也按最新事实
	if s := l.Summary(10, day7); s.AccruedPenalty != 15000 || s.OutstandingPrincipal != 1_000_000 {
		t.Fatalf("historical query at penalty moment: got %+v", s)
	}
}

// 更正或撤销末笔结清款：未偿本金、罚金与逾期判断立即变化。
func TestCorrectAndRevokeFinalPayment(t *testing.T) {
	setup := func() *Ledger {
		l := New()
		l.RegisterInstallment(Installment{ID: 1, Buyer: 10, Effective: testBase, Due: testBase, Principal: 100_000})
		l.PutRepayment(Repayment{ID: 100, InstallmentID: 1, Amount: 60_000, Time: testBase + daySeconds})
		l.PutRepayment(Repayment{ID: 101, InstallmentID: 1, Amount: 40_000, Time: testBase + 20*daySeconds})
		return l
	}
	q := testBase + 30*daySeconds

	// 已结清：第 7、14 天各按 40000 计罚 600，共 1200
	l := setup()
	if s := l.Summary(10, q); s.OutstandingPrincipal != 0 || s.AccruedPenalty != 1200 || s.HasOverdue15 {
		t.Fatalf("fully paid: got %+v", s)
	}

	// 撤销末笔结清款：本金回到 40000，第 21、28 天继续计罚，逾期成立
	l = setup()
	l.RevokeRepayment(101)
	want := Summary{OutstandingPrincipal: 40_000, AccruedPenalty: 2400, HasOverdue15: true}
	if s := l.Summary(10, q); s != want {
		t.Fatalf("after revoke: got %+v want %+v", s, want)
	}

	// 更正末笔时间提前到第 10 天：第 14 天起不再计罚，逾期窗口结束于第 10 天
	l = setup()
	l.PutRepayment(Repayment{ID: 101, InstallmentID: 1, Amount: 40_000, Time: testBase + 10*daySeconds})
	if s := l.Summary(10, q); s.OutstandingPrincipal != 0 || s.AccruedPenalty != 600 {
		t.Fatalf("after moving payoff earlier: got %+v", s)
	}
	if s := l.Summary(10, testBase+20*daySeconds); s.HasOverdue15 {
		t.Fatalf("should not be overdue after payoff at day 10: got %+v", s)
	}
	// 末笔在第 20 天结清时，第 16 天处于逾期窗口且本金未清，应为逾期
	l = setup()
	if s := l.Summary(10, testBase+16*daySeconds); !s.HasOverdue15 {
		t.Fatalf("should be overdue at day 16 before payoff: got %+v", s)
	}
}

// 分币舍入与累计封顶：触及上限的那笔只收剩余额度。
func TestRoundingAndCap(t *testing.T) {
	// 本金 500 分：每次计罚 (500*15+500)/1000 = 8 分，上限 (500*12+50)/100 = 60 分；
	// 前 7 次累计 56，第 8 次应收 8 但只收剩余额度 4。
	l := New()
	l.RegisterInstallment(Installment{ID: 1, Buyer: 10, Effective: testBase, Due: testBase, Principal: 500})
	if s := l.Summary(10, testBase+49*daySeconds); s.AccruedPenalty != 56 {
		t.Fatalf("after 7 penalties: got %+v", s)
	}
	if s := l.Summary(10, testBase+56*daySeconds); s.AccruedPenalty != 60 {
		t.Fatalf("cap should truncate the 8th penalty to 4: got %+v", s)
	}
	if s := l.Summary(10, testBase+365*daySeconds); s.AccruedPenalty != 60 {
		t.Fatalf("no more penalty after day 56: got %+v", s)
	}

	// 本金 100 分：每次 1.5 分四舍五入为 2 分，上限 12 分
	l = New()
	l.RegisterInstallment(Installment{ID: 1, Buyer: 10, Effective: testBase, Due: testBase, Principal: 100})
	if s := l.Summary(10, testBase+7*daySeconds); s.AccruedPenalty != 2 {
		t.Fatalf("1.5 rounds half up to 2: got %+v", s)
	}
	if s := l.Summary(10, testBase+56*daySeconds); s.AccruedPenalty != 12 {
		t.Fatalf("cap 12: got %+v", s)
	}

	// 本金 333 分：每次 4.995 分舍为 5 分，上限 40 分，8 次恰好 40
	l = New()
	l.RegisterInstallment(Installment{ID: 1, Buyer: 10, Effective: testBase, Due: testBase, Principal: 333})
	if s := l.Summary(10, testBase+56*daySeconds); s.AccruedPenalty != 40 {
		t.Fatalf("cap 40 reached exactly: got %+v", s)
	}

	// 本金 30 分：每次 0.45 分舍为 0，不产生罚金
	l = New()
	l.RegisterInstallment(Installment{ID: 1, Buyer: 10, Effective: testBase, Due: testBase, Principal: 30})
	if s := l.Summary(10, testBase+56*daySeconds); s.AccruedPenalty != 0 {
		t.Fatalf("0.45 rounds down to 0: got %+v", s)
	}
}

// 逾期 15 天边界与未生效分期不计入。
func TestOverdueBoundaryAndEffective(t *testing.T) {
	l := New()
	l.RegisterInstallment(Installment{ID: 1, Buyer: 10, Effective: testBase, Due: testBase, Principal: 1000})
	if s := l.Summary(10, testBase+15*daySeconds-1); s.HasOverdue15 {
		t.Fatalf("14d23h59m59s should not be overdue: got %+v", s)
	}
	if s := l.Summary(10, testBase+15*daySeconds); !s.HasOverdue15 {
		t.Fatalf("exactly 15 days should be overdue: got %+v", s)
	}
	// 恰好在第 15 天结清：该时刻本金已清，不算逾期
	l.PutRepayment(Repayment{ID: 100, InstallmentID: 1, Amount: 1000, Time: testBase + 15*daySeconds})
	if s := l.Summary(10, testBase+15*daySeconds); s.HasOverdue15 {
		t.Fatalf("paid off exactly at day 15: got %+v", s)
	}

	// 尚未生效的分期不计入汇总
	l2 := New()
	l2.RegisterInstallment(Installment{ID: 1, Buyer: 10, Effective: testBase + 100*daySeconds, Due: testBase, Principal: 5000})
	if s := l2.Summary(10, testBase+99*daySeconds); s != (Summary{}) {
		t.Fatalf("not yet effective: got %+v", s)
	}
	if s := l2.Summary(10, testBase+100*daySeconds); s.OutstandingPrincipal != 5000 {
		t.Fatalf("effective now: got %+v", s)
	}
}

// 随机差分测试：Ledger 的增量索引结果须与按最新事实从头逐笔计算的暴力实现一致。
func TestDifferentialRandom(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	l := New()
	bf := newBruteForce()
	buyers := []int64{1, 2, 3}

	const nInst = 60
	var instIDs []int64
	for i := 0; i < nInst; i++ {
		effective := testBase + int64(rng.Intn(30))*daySeconds
		inst := Installment{
			ID:        int64(1000 + i),
			Buyer:     buyers[rng.Intn(len(buyers))],
			Effective: effective,
			Due:       effective + int64(rng.Intn(40))*daySeconds,
			Principal: 1_000_000 + int64(rng.Intn(1_000_000)),
		}
		l.RegisterInstallment(inst)
		bf.register(inst)
		instIDs = append(instIDs, inst.ID)
	}

	check := func(step int) {
		for q := 0; q < 3; q++ {
			at := testBase + int64(rng.Intn(120))*daySeconds + int64(rng.Intn(86400))
			buyer := buyers[rng.Intn(len(buyers))]
			got := l.Summary(buyer, at)
			want := bf.summary(buyer, at)
			if got != want {
				t.Fatalf("step %d: buyer=%d at=%d got %+v want %+v", step, buyer, at, got, want)
			}
		}
	}

	var repIDs []int64
	nextRep := int64(1)
	for step := 0; step < 400; step++ {
		switch {
		case len(repIDs) == 0 || rng.Intn(100) < 55:
			inst := bf.installments[instIDs[rng.Intn(len(instIDs))]]
			rep := Repayment{
				ID:            nextRep,
				InstallmentID: inst.ID,
				Amount:        1 + int64(rng.Intn(1000)),
				Time:          inst.Effective + int64(rng.Intn(80))*daySeconds + int64(rng.Intn(86400)),
			}
			nextRep++
			repIDs = append(repIDs, rep.ID)
			l.PutRepayment(rep)
			bf.put(rep)
		case rng.Intn(100) < 60:
			// 更正：改变金额或生效时刻
			id := repIDs[rng.Intn(len(repIDs))]
			old := bf.repayments[id]
			inst := bf.installments[old.InstallmentID]
			rep := Repayment{
				ID:            id,
				InstallmentID: old.InstallmentID,
				Amount:        1 + int64(rng.Intn(1000)),
				Time:          inst.Effective + int64(rng.Intn(80))*daySeconds + int64(rng.Intn(86400)),
			}
			l.PutRepayment(rep)
			bf.put(rep)
		default:
			// 撤销
			i := rng.Intn(len(repIDs))
			id := repIDs[i]
			repIDs = append(repIDs[:i], repIDs[i+1:]...)
			l.RevokeRepayment(id)
			bf.revoke(id)
		}
		check(step)
	}
}

// 密集还款分期：一笔分期上大量小额还款的更正与撤销。
func TestDenseRepaymentsDifferential(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	l := New()
	bf := newBruteForce()
	inst := Installment{ID: 1, Buyer: 9, Effective: testBase, Due: testBase + 10*daySeconds, Principal: 5_000_000}
	l.RegisterInstallment(inst)
	bf.register(inst)

	var repIDs []int64
	for i := 0; i < 200; i++ {
		rep := Repayment{
			ID:            int64(5000 + i),
			InstallmentID: 1,
			Amount:        1 + int64(rng.Intn(100)),
			Time:          testBase + int64(rng.Intn(70))*daySeconds + int64(rng.Intn(86400)),
		}
		repIDs = append(repIDs, rep.ID)
		l.PutRepayment(rep)
		bf.put(rep)
	}
	for step := 0; step < 100; step++ {
		id := repIDs[rng.Intn(len(repIDs))]
		if rng.Intn(2) == 0 {
			rep := Repayment{
				ID:            id,
				InstallmentID: 1,
				Amount:        1 + int64(rng.Intn(100)),
				Time:          testBase + int64(rng.Intn(70))*daySeconds + int64(rng.Intn(86400)),
			}
			l.PutRepayment(rep)
			bf.put(rep)
		} else {
			l.RevokeRepayment(id)
			bf.revoke(id)
		}
		for q := 0; q < 4; q++ {
			at := testBase + int64(rng.Intn(90))*daySeconds + int64(rng.Intn(86400))
			if got, want := l.Summary(9, at), bf.summary(9, at); got != want {
				t.Fatalf("step %d at=%d: got %+v want %+v", step, at, got, want)
			}
		}
	}
}
