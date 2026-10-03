package credit

import (
	"math"
	"math/rand"
	"testing"
)

// ---------- 独立 oracle：按最新事实从头逐笔计算 ----------

type oracleInstallment struct {
	buyer     uint64
	effective int64
	due       int64
	principal int64
}

type oraclePayment struct {
	installment uint64
	at          int64
	amount      int64
	revoked     bool
}

type oracle struct {
	installments map[uint64]*oracleInstallment
	payments     map[uint64]*oraclePayment
}

func newOracle() *oracle {
	return &oracle{
		installments: make(map[uint64]*oracleInstallment),
		payments:     make(map[uint64]*oraclePayment),
	}
}

func (o *oracle) paidUpTo(instID uint64, at int64) int64 {
	var s int64
	for _, p := range o.payments {
		if !p.revoked && p.installment == instID && p.at <= at {
			s += p.amount
		}
	}
	return s
}

func (o *oracle) query(buyer uint64, at int64) Summary {
	var sum Summary
	for id, in := range o.installments {
		if in.buyer != buyer || in.effective > at {
			continue
		}
		paid := o.paidUpTo(id, at)
		sum.OutstandingCents += in.principal - paid

		capAmt := (in.principal*PenaltyCapNum + PenaltyCapDen/2) / PenaltyCapDen
		var cum int64
		for k := int64(1); k <= PenaltyMaxTimes; k++ {
			m := in.due + k*PenaltyIntervalDays*SecondsPerDay
			if m > at || m < in.effective {
				continue
			}
			out := in.principal - o.paidUpTo(id, m)
			raw := (out*PenaltyRateNum + PenaltyRateDen/2) / PenaltyRateDen
			if cum+raw > capAmt {
				raw = capAmt - cum
			}
			cum += raw
		}
		sum.PenaltyCents += cum

		start := in.due + OverdueDays*SecondsPerDay
		if in.effective > start {
			start = in.effective
		}
		if at >= start && paid < in.principal {
			sum.Overdue15 = true
		}
	}
	return sum
}

// activePaid 返回某分期未撤销还款总额。
func (o *oracle) activePaid(instID uint64) int64 {
	var s int64
	for _, p := range o.payments {
		if !p.revoked && p.installment == instID {
			s += p.amount
		}
	}
	return s
}

// ---------- 用例 ----------

const day = SecondsPerDay

func mustAdd(t *testing.T, l *Ledger, o *oracle, id, buyer uint64, eff, due, principal int64) {
	t.Helper()
	if err := l.AddInstallment(id, buyer, eff, due, principal); err != nil {
		t.Fatalf("AddInstallment(%d): %v", id, err)
	}
	o.installments[id] = &oracleInstallment{buyer: buyer, effective: eff, due: due, principal: principal}
}

func mustPay(t *testing.T, l *Ledger, o *oracle, payID, instID uint64, at, amount int64) {
	t.Helper()
	if err := l.RecordPayment(payID, instID, at, amount); err != nil {
		t.Fatalf("RecordPayment(%d): %v", payID, err)
	}
	o.payments[payID] = &oraclePayment{installment: instID, at: at, amount: amount}
}

func checkQuery(t *testing.T, l *Ledger, o *oracle, buyer uint64, at int64) {
	t.Helper()
	got := l.Query(buyer, at)
	want := o.query(buyer, at)
	if got != want {
		t.Fatalf("Query(buyer=%d, at=%d) = %+v, want %+v", buyer, at, got, want)
	}
}

// 还款跨过计罚时刻：同秒还款先于计罚生效。
func TestPaymentCrossingPenaltyMoment(t *testing.T) {
	l := NewLedger()
	o := newOracle()
	mustAdd(t, l, o, 1, 10, 0, 100*day, 100000) // due = 第100天

	// 第 106 天（第 1 个计罚时刻前一秒）查询：无罚金。
	checkQuery(t, l, o, 10, 106*day)
	// 第 107 天：第 1 次计罚，1500。
	checkQuery(t, l, o, 10, 107*day)

	// 在恰好的计罚时刻（第 114 天）还款 40000：同秒还款先于计罚。
	mustPay(t, l, o, 1, 1, 114*day, 40000)
	checkQuery(t, l, o, 10, 114*day)
	checkQuery(t, l, o, 10, 121*day)
	checkQuery(t, l, o, 10, 200*day)

	// 历史时刻也不受后续还款影响。
	checkQuery(t, l, o, 10, 107*day)
	checkQuery(t, l, o, 10, 113*day)
}

// 更正与撤销末笔结清款：结清时刻移动，逾期标记与罚金随之变化。
func TestCorrectAndRevokeFinalPayment(t *testing.T) {
	l := NewLedger()
	o := newOracle()
	mustAdd(t, l, o, 1, 10, 0, 100*day, 50000)
	mustPay(t, l, o, 1, 1, 50*day, 20000)
	mustPay(t, l, o, 2, 1, 110*day, 30000) // 末笔结清款

	for _, at := range []int64{0, 100 * day, 115 * day, 200 * day} {
		checkQuery(t, l, o, 10, at)
	}

	// 更正末笔：金额减少，不再结清。
	if err := l.CorrectPayment(2, 110*day, 10000); err != nil {
		t.Fatal(err)
	}
	o.payments[2].amount = 10000
	for _, at := range []int64{100 * day, 115 * day, 116 * day, 300 * day} {
		checkQuery(t, l, o, 10, at)
	}

	// 更正末笔：时间移到逾期 15 天之后，逾期标记应回溯出现。
	if err := l.CorrectPayment(2, 120*day, 30000); err != nil {
		t.Fatal(err)
	}
	o.payments[2].at = 120 * day
	o.payments[2].amount = 30000
	for _, at := range []int64{114 * day, 115 * day, 119 * day, 120 * day, 300 * day} {
		checkQuery(t, l, o, 10, at)
	}

	// 撤销末笔结清款：视为从未发生。
	if err := l.RevokePayment(2); err != nil {
		t.Fatal(err)
	}
	o.payments[2].revoked = true
	for _, at := range []int64{110 * day, 115 * day, 120 * day, 156 * day, 400 * day} {
		checkQuery(t, l, o, 10, at)
	}
}

// 分币舍入触及封顶：本金 34 分，每次计罚 1 分，封顶 4 分。
func TestRoundingAndCap(t *testing.T) {
	l := NewLedger()
	o := newOracle()
	mustAdd(t, l, o, 1, 10, 0, 0, 34)

	// 第 56 天后不再增加；累计罚金被 12% 封顶（4 分）。
	for _, at := range []int64{6 * day, 7 * day, 14 * day, 28 * day, 29 * day, 35 * day, 56 * day, 1000 * day} {
		checkQuery(t, l, o, 10, at)
	}
	got := l.Query(10, 1000*day)
	if got.PenaltyCents != 4 {
		t.Fatalf("penalty = %d, want 4 (capped)", got.PenaltyCents)
	}

	// 部分还款后未偿本金 33 分，1.5% 四舍五入仍为 1 分。
	mustPay(t, l, o, 1, 1, 3*day, 1)
	for _, at := range []int64{3 * day, 7 * day, 1000 * day} {
		checkQuery(t, l, o, 10, at)
	}
}

// 随机操作对照 oracle：覆盖补录、更正（金额与时间）、撤销与历史时刻查询。
func TestRandomAgainstOracle(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	l := NewLedger()
	o := newOracle()

	const buyers = 4
	const installments = 60
	for i := uint64(1); i <= installments; i++ {
		eff := rng.Int63n(500 * day)
		due := eff + rng.Int63n(300*day)
		principal := 1 + rng.Int63n(200000)
		mustAdd(t, l, o, i, uint64(i%buyers), eff, due, principal)
	}

	var payIDs []uint64
	nextPay := uint64(1)
	randTime := func() int64 { return rng.Int63n(1200 * day) }

	for op := 0; op < 3000; op++ {
		switch rng.Intn(10) {
		case 0, 1, 2, 3, 4: // 写入还款
			instID := uint64(1 + rng.Intn(installments))
			in := o.installments[instID]
			room := in.principal - o.activePaid(instID)
			if room <= 0 {
				continue
			}
			amount := int64(1 + rng.Int63n(room))
			at := in.effective + rng.Int63n(800*day)
			mustPay(t, l, o, nextPay, instID, at, amount)
			payIDs = append(payIDs, nextPay)
			nextPay++
		case 5, 6, 7: // 更正
			if len(payIDs) == 0 {
				continue
			}
			id := payIDs[rng.Intn(len(payIDs))]
			p := o.payments[id]
			if p.revoked {
				continue
			}
			in := o.installments[p.installment]
			room := in.principal - o.activePaid(p.installment) + p.amount
			amount := int64(1 + rng.Int63n(room))
			at := in.effective + rng.Int63n(800*day)
			if err := l.CorrectPayment(id, at, amount); err != nil {
				t.Fatalf("CorrectPayment(%d): %v", id, err)
			}
			p.at, p.amount = at, amount
		case 8: // 撤销
			if len(payIDs) == 0 {
				continue
			}
			id := payIDs[rng.Intn(len(payIDs))]
			p := o.payments[id]
			if p.revoked {
				continue
			}
			if err := l.RevokePayment(id); err != nil {
				t.Fatalf("RevokePayment(%d): %v", id, err)
			}
			p.revoked = true
		case 9: // 重新写入已撤销的还款
			if len(payIDs) == 0 {
				continue
			}
			id := payIDs[rng.Intn(len(payIDs))]
			p := o.payments[id]
			if !p.revoked {
				continue
			}
			in := o.installments[p.installment]
			room := in.principal - o.activePaid(p.installment)
			if room <= 0 {
				continue
			}
			amount := int64(1 + rng.Int63n(room))
			at := in.effective + rng.Int63n(800*day)
			mustPay(t, l, o, id, p.installment, at, amount)
		}

		// 每次操作后查询随机买家与随机历史时刻（含修订前的时刻）。
		for q := 0; q < 3; q++ {
			checkQuery(t, l, o, uint64(rng.Intn(buyers)), randTime())
		}
	}

	// 收尾全量比对。
	for b := uint64(0); b < buyers; b++ {
		for at := int64(0); at <= 1200*day; at += 97 * day {
			checkQuery(t, l, o, b, at)
		}
	}
}

// 边界：查询未知买家、尚未生效的分期、结清后罚金保留。
func TestBoundaries(t *testing.T) {
	l := NewLedger()
	if got := l.Query(999, 0); got != (Summary{}) {
		t.Fatalf("unknown buyer: %+v", got)
	}
	o := newOracle()
	mustAdd(t, l, o, 1, 10, 1000*day, 1100*day, 8000)
	checkQuery(t, l, o, 10, 500*day) // 尚未生效
	mustPay(t, l, o, 1, 1, 1000*day, 8000)
	checkQuery(t, l, o, 10, 1000*day)
	// 结清后罚金仍应保留：先在第 1107 天产生罚金，再结清。
	mustAdd(t, l, o, 2, 10, 0, 100*day, 8000)
	mustPay(t, l, o, 2, 2, 120*day, 8000)
	checkQuery(t, l, o, 10, 120*day)
	checkQuery(t, l, o, 10, math.MaxInt64-2*day)
}
