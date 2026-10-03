package credit

import (
	"math/rand"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// groupedOracle 按分期分组还款的独立核算器，用于性能测试中快速核验。
type groupedOracle struct {
	insts map[uint64]*oracleInstallment
	pays  map[uint64][]oraclePayment // 按分期分组
}

func newGroupedOracle() *groupedOracle {
	return &groupedOracle{
		insts: make(map[uint64]*oracleInstallment),
		pays:  make(map[uint64][]oraclePayment),
	}
}

func (g *groupedOracle) paidUpTo(instID uint64, at int64) int64 {
	var s int64
	for _, p := range g.pays[instID] {
		if !p.revoked && p.at <= at {
			s += p.amount
		}
	}
	return s
}

func (g *groupedOracle) query(buyer uint64, at int64) Summary {
	var sum Summary
	for id, in := range g.insts {
		if in.buyer != buyer || in.effective > at {
			continue
		}
		paid := g.paidUpTo(id, at)
		sum.OutstandingCents += in.principal - paid

		capAmt := (in.principal*PenaltyCapNum + PenaltyCapDen/2) / PenaltyCapDen
		var cum int64
		for k := int64(1); k <= PenaltyMaxTimes; k++ {
			m := in.due + k*PenaltyIntervalDays*SecondsPerDay
			if m > at || m < in.effective {
				continue
			}
			out := in.principal - g.paidUpTo(id, m)
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

func maxRSSBytes() uint64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return uint64(ru.Maxrss) // darwin 上单位为字节
}

// TestPerformance 规模验收：10 万期、30 万条还款（一个买家 5 万期，一笔分期 10 万条还款），
// 初始装载后连续 2 万次更正、每次紧接 2 次不同历史时刻查询，核验结果并报告耗时与内存。
func TestPerformance(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping performance test in short mode")
	}
	rng := rand.New(rand.NewSource(7))

	const (
		bigBuyer     = uint64(1)
		verifyBuyer  = uint64(2)
		nInst        = 100000
		bigInsts     = 50000 // 大买家分期数
		verifyInsts  = 200
		nPays        = 300000
		longInstPays = 100000 // 长流水分期的还款条数
		nCorrections = 20000
		longCorr     = 10000 // 针对长流水分期的更正数
	)

	l := NewLedger()
	g := newGroupedOracle()

	// ---------- 初始装载 ----------
	loadStart := time.Now()

	// 大买家的 5 万期；分期 1 为长流水分期。
	instEff := make([]int64, nInst+1)
	instDue := make([]int64, nInst+1)
	instPrin := make([]int64, nInst+1)
	instBuyer := make([]uint64, nInst+1)
	for i := uint64(1); i <= nInst; i++ {
		var buyer uint64
		switch {
		case i <= bigInsts:
			buyer = bigBuyer
		case i <= bigInsts+verifyInsts:
			buyer = verifyBuyer
		default:
			buyer = 3 + uint64(rng.Intn(100))
		}
		eff := rng.Int63n(400 * day)
		due := eff + (30 + rng.Int63n(270))
		principal := int64(1000 + rng.Int63n(1000000))
		if i == 1 {
			principal = 1 << 40 // 长流水分期：容纳 10 万条小额还款
		}
		if err := l.AddInstallment(i, buyer, eff, due, principal); err != nil {
			t.Fatal(err)
		}
		g.insts[i] = &oracleInstallment{buyer: buyer, effective: eff, due: due, principal: principal}
		instEff[i], instDue[i], instPrin[i], instBuyer[i] = eff, due, principal, buyer
	}

	// 还款：payID 从 1 起连续编号。
	payInst := make([]uint64, nPays+1)
	payAt := make([]int64, nPays+1)
	payAmt := make([]int64, nPays+1)
	payIdx := make([]int, nPays+1) // 还款在 g.pays[instID] 中的下标
	paidSoFar := make(map[uint64]int64)
	addPay := func(payID int, instID uint64, at, amount int64) {
		if err := l.RecordPayment(uint64(payID), instID, at, amount); err != nil {
			t.Fatalf("load RecordPayment(%d): %v", payID, err)
		}
		payInst[payID], payAt[payID], payAmt[payID] = instID, at, amount
		payIdx[payID] = len(g.pays[instID])
		g.pays[instID] = append(g.pays[instID], oraclePayment{installment: instID, at: at, amount: amount})
		paidSoFar[instID] += amount
	}

	pid := 1
	// 长流水分期：10 万条小额还款，铺满其历史窗口。
	for k := 0; k < longInstPays; k++ {
		at := instEff[1] + rng.Int63n(800*day)
		addPay(pid, 1, at, 1+rng.Int63n(10000))
		pid++
	}
	// 核验买家：200 期各 10 条。
	for i := uint64(bigInsts + 1); i <= bigInsts+verifyInsts; i++ {
		for k := 0; k < 10; k++ {
			room := instPrin[i] / 20
			addPay(pid, i, instEff[i]+rng.Int63n(800*day), 1+rng.Int63n(room))
			pid++
		}
	}
	// 其余还款分散到其余分期（含大买家的其他分期）。
	for pid <= nPays {
		instID := uint64(2 + rng.Intn(nInst-1))
		if instID >= bigInsts+1 && instID <= bigInsts+verifyInsts {
			continue // 核验买家的还款保持固定数量
		}
		room := instPrin[instID] - paidSoFar[instID]
		if room <= 1 {
			continue
		}
		amount := 1 + rng.Int63n(room/2+1)
		addPay(pid, instID, instEff[instID]+rng.Int63n(800*day), amount)
		pid++
	}
	loadElapsed := time.Since(loadStart)

	// ---------- 连续更正 + 即时查询 ----------
	var heapPeak uint64
	sampleMem := func() {
		var ms runtime.MemStats
		runtime.ReadMemStats(&ms)
		if v := ms.HeapInuse + ms.StackInuse; v > heapPeak {
			heapPeak = v
		}
	}
	sampleMem()

	var libElapsed time.Duration
	corrections := 0
	verify := func(buyer uint64, at int64) {
		got := l.Query(buyer, at)
		want := g.query(buyer, at)
		if got != want {
			t.Fatalf("correction %d: Query(buyer=%d, at=%d) = %+v, want %+v",
				corrections, buyer, at, got, want)
		}
	}

	for c := 0; c < nCorrections; c++ {
		var target int
		if c < longCorr {
			target = 1 + (c*9973)%longInstPays // 长流水分期的还款
		} else if c%5 == 0 {
			target = longInstPays + 1 + rng.Intn(verifyInsts*10) // 核验买家的还款
		} else {
			target = longInstPays + verifyInsts*10 + 1 + rng.Intn(nPays-longInstPays-verifyInsts*10)
		}
		instID := payInst[target]
		var newAt, newAmt int64
		if c%2 == 0 {
			// 金额变化
			newAt = payAt[target]
			room := instPrin[instID] - paidSoFar[instID] + payAmt[target]
			newAmt = 1 + rng.Int63n(room)
		} else {
			// 时间移动
			newAt = instEff[instID] + rng.Int63n(800*day)
			newAmt = payAmt[target]
		}
		paidSoFar[instID] += newAmt - payAmt[target]

		start := time.Now()
		if err := l.CorrectPayment(uint64(target), newAt, newAmt); err != nil {
			t.Fatalf("correction %d (pay %d): %v", c, target, err)
		}
		// 每次更正紧接两次不同历史时刻的查询。
		at1 := instEff[1] - 100*day + rng.Int63n(1000*day) // 覆盖还款历史内部及其前后
		sum1 := l.Query(bigBuyer, at1)
		at2 := instEff[bigInsts+1] - 50*day + rng.Int63n(1000*day)
		sum2 := l.Query(verifyBuyer, at2)
		libElapsed += time.Since(start)
		_ = sum1
		_ = sum2

		// 同步独立核算器并即时核验（不计入库耗时）。
		payAt[target], payAmt[target] = newAt, newAmt
		rec := &g.pays[instID][payIdx[target]]
		rec.at, rec.amount = newAt, newAmt
		verify(verifyBuyer, at2)

		corrections++
		if c%500 == 0 {
			sampleMem()
		}
	}
	sampleMem()

	// ---------- 收尾核验：大买家在多个历史时刻的汇总 ----------
	for _, at := range []int64{
		instEff[1] - 10*day,
		instEff[1] + 100*day,
		instEff[1] + 400*day,
		instEff[1] + 800*day,
		instEff[1] + 1200*day,
	} {
		got := l.Query(bigBuyer, at)
		want := g.query(bigBuyer, at)
		if got != want {
			t.Fatalf("final check at=%d: got %+v, want %+v", at, got, want)
		}
	}

	rss := maxRSSBytes()
	t.Logf("load: %v (100k installments, 300k payments)", loadElapsed)
	t.Logf("corrections+queries: %v (20k corrections, 40k queries, library time only)", libElapsed)
	t.Logf("peak heap+stack: %.1f MiB, peak RSS: %.1f MiB",
		float64(heapPeak)/MiB, float64(rss)/MiB)

	if libElapsed > 15*time.Second {
		t.Fatalf("corrections+queries took %v, exceeds 15s", libElapsed)
	}
	const limit = 512 * MiB
	if rss > limit {
		t.Fatalf("peak RSS %.1f MiB exceeds 512 MiB", float64(rss)/MiB)
	}
}

const MiB = 1 << 20
