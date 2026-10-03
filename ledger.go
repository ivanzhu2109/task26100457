// Package ledger 提供赊账分期历史核算能力。
//
// 数据仅保存在进程内存中，由上层串行调用。支持登记分期、写入或更正还款、
// 撤销还款，以及按最新事实查询买家在任意历史时刻的汇总。
//
// 计罚规则：从到期后第 7 天开始，每 7 天（固定 86400 秒）计罚一次，最多 8 次；
// 每次按计罚时刻未偿本金的 1.5% 四舍五入到分；累计罚金上限为原本金的 12%
// 四舍五入到分，触及上限的那笔只收剩余额度；第 56 天之后不再增加罚金，不计复利。
// 同秒发生的还款先于计罚生效；查询包含截止时刻发生的还款与计罚。
package ledger

import "math"

const (
	daySeconds      = 86400
	penaltyInterval = 7 * daySeconds // 计罚间隔
	penaltyTimes    = 8              // 最多计罚次数
	overdueDays     = 15             // 逾期判断天数
)

// Installment 描述一期赊账分期。登记后资料不可变。
type Installment struct {
	ID        int64 // 分期唯一标识
	Buyer     int64 // 所属买家标识
	Effective int64 // 生效时刻，UTC 秒
	Due       int64 // 到期时刻，UTC 秒
	Principal int64 // 本金，单位分
}

// Repayment 描述一笔还款，仅冲减本金。
type Repayment struct {
	ID            int64 // 还款唯一标识
	InstallmentID int64 // 所属分期，更正时不可改变
	Amount        int64 // 金额，单位分
	Time          int64 // 生效时刻，UTC 秒
}

// Summary 为买家在指定时刻的历史汇总。
type Summary struct {
	OutstandingPrincipal int64 // 未偿本金总额，单位分
	AccruedPenalty       int64 // 累计应计罚金，单位分
	HasOverdue15         bool  // 是否存在本金未清且逾期至少 15 天的分期
}

// installment 为分期的内部状态。
type installment struct {
	id          int64
	buyer       int64
	effective   int64
	due         int64
	principal   int64
	idx         int32     // 内部序号，用作 Treap 键的第二维
	reps        *sumTreap // 未撤销还款，键 (生效时刻, 还款内部序号)，值 金额
	totalRepaid int64     // 未撤销还款总额
	start       int64     // 逾期观察起点 = max(生效时刻, 到期+15天)
	tau         int64     // 本金结清时刻；未结清为 math.MaxInt64
	penEvents   [penaltyTimes]penEvent
	penCount    int // 当前已写入买家罚金事件树的条数
}

// penEvent 为一次计罚产生的增量罚金事件。
type penEvent struct {
	time  int64 // 计罚时刻
	delta int64 // 该次计罚的增量罚金（封顶后），单位分
}

// repayment 为还款的内部记录。
type repayment struct {
	id     int64
	inst   *installment
	amount int64
	time   int64
	idx    int32 // 内部序号
}

// buyerState 为单个买家的聚合索引。
type buyerState struct {
	installments map[int64]*installment
	principals   *sumTreap // 键 (生效时刻, 分期内部序号)，值 本金
	repays       *sumTreap // 键 (还款时刻, 还款内部序号)，值 金额
	penalties    *sumTreap // 键 (计罚时刻, 分期内部序号*8+次序)，值 增量罚金
	overdue      *maxTreap // 键 (逾期观察起点, 分期内部序号)，值 结清时刻 tau
}

func newBuyerState() *buyerState {
	return &buyerState{
		installments: make(map[int64]*installment),
		principals:   &sumTreap{},
		repays:       &sumTreap{},
		penalties:    &sumTreap{},
		overdue:      &maxTreap{},
	}
}

// Ledger 为核算库入口。所有方法须由上层串行调用。
type Ledger struct {
	buyers       map[int64]*buyerState
	installments map[int64]*installment // 按分期 ID 索引
	repayments   map[int64]*repayment   // 按还款 ID 索引（仅未撤销）
	nextInstIdx  int32
	nextRepIdx   int32
}

// New 创建一个空的 Ledger。
func New() *Ledger {
	return &Ledger{
		buyers:       make(map[int64]*buyerState),
		installments: make(map[int64]*installment),
		repayments:   make(map[int64]*repayment),
	}
}

// RegisterInstallment 登记一期分期。登记后其资料不可变；重复 ID 会被忽略。
func (l *Ledger) RegisterInstallment(inst Installment) {
	if _, ok := l.installments[inst.ID]; ok {
		return
	}
	b := l.buyers[inst.Buyer]
	if b == nil {
		b = newBuyerState()
		l.buyers[inst.Buyer] = b
	}
	it := &installment{
		id:        inst.ID,
		buyer:     inst.Buyer,
		effective: inst.Effective,
		due:       inst.Due,
		principal: inst.Principal,
		idx:       l.nextInstIdx,
		reps:      &sumTreap{},
		start:     maxInt64(inst.Effective, inst.Due+overdueDays*daySeconds),
		tau:       math.MaxInt64,
	}
	if inst.Principal <= 0 {
		// 本金为零视为始终结清，永不构成逾期。
		it.tau = math.MinInt64
	}
	l.nextInstIdx++
	l.installments[inst.ID] = it
	b.installments[inst.ID] = it
	b.principals.insert(treapKey{inst.Effective, int64(it.idx)}, inst.Principal)
	b.overdue.insert(treapKey{it.start, int64(it.idx)}, it.tau)
	l.refreshInstallment(it, b)
}

func maxInt64(x, y int64) int64 {
	if x > y {
		return x
	}
	return y
}
