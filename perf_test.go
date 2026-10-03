package ledger

import (
	"math/rand"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// perfReference 是性能测试专用的独立参照实现：
// 还款按分期分组，每次查询依据最新事实从头逐笔计算。
type perfReference struct {
	installments map[int64]Installment
	byBuyer      map[int64][]int64             // 买家 -> 分期 ID 列表
	repsByInst   map[int64]map[int64]Repayment // 分期 ID -> 还款 ID -> 还款
}

func newPerfReference() *perfReference {
	return &perfReference{
		installments: make(map[int64]Installment),
		byBuyer:      make(map[int64][]int64),
		repsByInst:   make(map[int64]map[int64]Repayment),
	}
}

func (pr *perfReference) register(inst Installment) {
	pr.installments[inst.ID] = inst
	pr.byBuyer[inst.Buyer] = append(pr.byBuyer[inst.Buyer], inst.ID)
}

func (pr *perfReference) put(rep Repayment) {
	m := pr.repsByInst[rep.InstallmentID]
	if m == nil {
		m = make(map[int64]Repayment)
		pr.repsByInst[rep.InstallmentID] = m
	}
	if old, ok := m[rep.ID]; ok {
		rep.InstallmentID = old.InstallmentID
		m = pr.repsByInst[old.InstallmentID]
	}
	m[rep.ID] = rep
}

func (pr *perfReference) summary(buyer int64, at int64) Summary {
	var out Summary
	for _, instID := range pr.byBuyer[buyer] {
		inst := pr.installments[instID]
		if inst.Effective > at {
			continue
		}
		reps := pr.repsByInst[instID]
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
			cum += roundHalfUp15permille(inst.Principal - repaidAt(p))
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

func maxRSSBytes() uint64 {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	if runtime.GOOS == "darwin" {
		return uint64(ru.Maxrss) // 字节
	}
	return uint64(ru.Maxrss) * 1024 // Linux 为 KB
}

// 性能验收：10 万期、30 万条还款；一个买家 5 万期，一笔分期 10 万条小额还款。
// 初始装载后连续 2 万次更正（1 万针对长流水分期），每次紧接两次不同历史时刻查询，
// 其中至少 2 万次查询针对大买家；总耗时须 < 15 秒，峰值内存 < 512 MiB。
func TestPerformanceAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}
	rng := rand.New(rand.NewSource(2026))
	const (
		nInst        = 100_000
		nBigInst     = 50_000
		nRep         = 300_000
		nDenseRep    = 100_000
		nCorrections = 20_000
		bigBuyer     = int64(1)
		nOtherBuyer  = 500
	)
	base := int64(1_700_000_000)

	l := New()
	ref := newPerfReference()

	// ---- 初始装载：10 万期 ----
	insts := make([]Installment, 0, nInst)
	for i := 0; i < nInst; i++ {
		buyer := bigBuyer
		if i >= nBigInst {
			buyer = int64(2 + i%nOtherBuyer)
		}
		effective := base + int64(i%365)*daySeconds
		principal := int64(2_000_000)
		if i == 0 {
			// 长流水分期需容纳 10 万条小额还款，总额不超过本金
			principal = 10_000_000
		}
		inst := Installment{
			ID:        int64(i + 1),
			Buyer:     buyer,
			Effective: effective,
			Due:       effective + 30*daySeconds,
			Principal: principal,
		}
		insts = append(insts, inst)
		l.RegisterInstallment(inst)
		ref.register(inst)
	}
	denseInst := insts[0] // 大买家的长流水分期

	// ---- 初始装载：30 万条还款，其中 10 万条挂在长流水分期上 ----
	denseRepIDs := make([]int64, 0, nDenseRep)
	sparseRepIDs := make([]int64, 0, nRep-nDenseRep)
	repOf := make(map[int64]Repayment, nRep)
	put := func(rep Repayment) {
		l.PutRepayment(rep)
		ref.put(rep)
		repOf[rep.ID] = rep
	}
	for i := 0; i < nDenseRep; i++ {
		rep := Repayment{
			ID:            int64(i + 1),
			InstallmentID: denseInst.ID,
			Amount:        1 + int64(rng.Intn(50)),
			Time:          denseInst.Effective + int64(rng.Intn(60))*daySeconds + int64(rng.Intn(86400)),
		}
		denseRepIDs = append(denseRepIDs, rep.ID)
		put(rep)
	}
	for i := nDenseRep; i < nRep; i++ {
		inst := insts[rng.Intn(nInst)]
		rep := Repayment{
			ID:            int64(i + 1),
			InstallmentID: inst.ID,
			Amount:        1 + int64(rng.Intn(500)),
			Time:          inst.Effective + int64(rng.Intn(60))*daySeconds + int64(rng.Intn(86400)),
		}
		sparseRepIDs = append(sparseRepIDs, rep.ID)
		put(rep)
	}

	var mem runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&mem)
	t.Logf("装载完成: HeapAlloc=%d MiB, MaxRSS=%d MiB", mem.HeapAlloc>>20, maxRSSBytes()>>20)

	// ---- 连续更正 + 即时查询 ----
	peakHeap := uint64(0)
	var timed time.Duration
	var bigBuyerQueries int
	checkpoints := 0
	for k := 0; k < nCorrections; k++ {
		// 前一半针对长流水分期，其余针对分散的分期
		var id int64
		if k < nCorrections/2 {
			id = denseRepIDs[k%nDenseRep]
		} else {
			id = sparseRepIDs[k%len(sparseRepIDs)]
		}
		old := repOf[id]
		inst := ref.installments[old.InstallmentID]
		rep := old
		maxAmount := 500
		if old.InstallmentID == denseInst.ID {
			// 长流水分期保持小额还款，保证总额不超过本金
			maxAmount = 50
		}
		switch k % 3 {
		case 0: // 仅改金额
			rep.Amount = 1 + int64(rng.Intn(maxAmount))
		case 1: // 仅移动时间
			rep.Time = inst.Effective + int64(rng.Intn(80))*daySeconds + int64(rng.Intn(86400))
		default: // 同时改金额与时间
			rep.Amount = 1 + int64(rng.Intn(maxAmount))
			rep.Time = inst.Effective + int64(rng.Intn(80))*daySeconds + int64(rng.Intn(86400))
		}

		// 查询时刻覆盖还款历史内部及其前后
		at1 := base - 10*daySeconds + int64(rng.Intn(510))*daySeconds + int64(rng.Intn(86400))
		at2 := base - 10*daySeconds + int64(rng.Intn(510))*daySeconds + int64(rng.Intn(86400))
		buyer2 := bigBuyer
		if k%2 == 1 {
			buyer2 = int64(2 + rng.Intn(nOtherBuyer))
		}

		start := time.Now()
		l.PutRepayment(rep)
		got1 := l.Summary(bigBuyer, at1)
		got2 := l.Summary(buyer2, at2)
		timed += time.Since(start)

		repOf[id] = rep
		ref.put(rep)
		bigBuyerQueries++
		if buyer2 == bigBuyer {
			bigBuyerQueries++
		}

		// 抽样核验：即时查询结果须与按最新事实从头逐笔计算一致
		if k%500 == 0 {
			checkpoints++
			if want := ref.summary(bigBuyer, at1); got1 != want {
				t.Fatalf("correction %d query1 buyer=%d at=%d: got %+v want %+v", k, bigBuyer, at1, got1, want)
			}
			if want := ref.summary(buyer2, at2); got2 != want {
				t.Fatalf("correction %d query2 buyer=%d at=%d: got %+v want %+v", k, buyer2, at2, got2, want)
			}
		}
		if k%2000 == 0 {
			runtime.ReadMemStats(&mem)
			if mem.HeapAlloc > peakHeap {
				peakHeap = mem.HeapAlloc
			}
		}
	}
	runtime.ReadMemStats(&mem)
	if mem.HeapAlloc > peakHeap {
		peakHeap = mem.HeapAlloc
	}

	const mib = 1 << 20
	t.Logf("连续 %d 次更正 + %d 次查询（其中大买家查询 %d 次，核验点 %d 个）",
		nCorrections, 2*nCorrections, bigBuyerQueries, checkpoints)
	t.Logf("总耗时: %v", timed)
	t.Logf("峰值内存: HeapAlloc=%d MiB, MaxRSS=%d MiB", peakHeap/mib, maxRSSBytes()/mib)

	if bigBuyerQueries < 20_000 {
		t.Fatalf("大买家查询次数不足: %d", bigBuyerQueries)
	}
	if timed > 15*time.Second {
		t.Fatalf("总耗时超过 15 秒: %v", timed)
	}
	if peakHeap > 512*mib {
		t.Fatalf("峰值堆内存超过 512 MiB: %d MiB", peakHeap/mib)
	}
	if rss := maxRSSBytes(); rss > 512*mib {
		t.Fatalf("峰值 RSS 超过 512 MiB: %d MiB", rss/mib)
	}
}
