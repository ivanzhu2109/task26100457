package credit

// 事件树分量约定：0=登记本金 1=还款 2=罚金 3=逾期标记计数。
const (
	compPrincipal = 0
	compPayment   = 1
	compPenalty   = 2
	compFlag      = 3
)

// payment 是一条还款记录。
type payment struct {
	installment uint64
	at          int64
	amount      int64
	revoked     bool
	seq         int64 // 在分期还款树与买家事件树中使用的序号
}

// buyerState 汇总一个买家所有分期产生的事件。
type buyerState struct {
	events *evtTreap
}

// Ledger 是赊账分期历史核算库的主对象。调用方需串行访问。
type Ledger struct {
	installments map[uint64]*installment
	payments     map[uint64]*payment
	buyers       map[uint64]*buyerState
	nextSeq      int64
}

// NewLedger 创建一个空的核算库。
func NewLedger() *Ledger {
	return &Ledger{
		installments: make(map[uint64]*installment),
		payments:     make(map[uint64]*payment),
		buyers:       make(map[uint64]*buyerState),
		nextSeq:      1,
	}
}

func (l *Ledger) allocSeq() int64 {
	s := l.nextSeq
	l.nextSeq++
	return s
}

func (l *Ledger) buyer(id uint64) *buyerState {
	bs, ok := l.buyers[id]
	if !ok {
		bs = &buyerState{events: newEvtTreap(id*0x9e3779b9 + 7)}
		l.buyers[id] = bs
	}
	return bs
}

// AddInstallment 登记一期分期。登记后资料不可变。
func (l *Ledger) AddInstallment(id, buyer uint64, effective, due, principalCents int64) error {
	if _, ok := l.installments[id]; ok {
		return ErrInstallmentExists
	}
	if principalCents <= 0 {
		return ErrInvalidAmount
	}
	bs := l.buyer(buyer)
	in := &installment{
		id:        id,
		buyer:     buyer,
		effective: effective,
		due:       due,
		principal: principalCents,
		pays:      newEvtTreap(id*0x85ebca6b + 13),
		prinSeq:   l.allocSeq(),
		flagSeq:   l.allocSeq(),
		endSeq:    l.allocSeq(),
	}
	for i := range in.penSeqs {
		in.penSeqs[i].seq = l.allocSeq()
	}
	bs.events.add(effective, in.prinSeq, [4]int64{principalCents, 0, 0, 0})
	l.installments[id] = in
	l.recompute(in)
	return nil
}

// RecordPayment 写入一条还款。payID 全局唯一；仅冲减本金。
// 若 payID 曾被撤销，视为重新写入一笔新还款。
func (l *Ledger) RecordPayment(payID uint64, installmentID uint64, at, amountCents int64) error {
	in, ok := l.installments[installmentID]
	if !ok {
		return ErrInstallmentNotFound
	}
	if amountCents <= 0 {
		return ErrInvalidAmount
	}
	p, exists := l.payments[payID]
	if exists && !p.revoked {
		return ErrPaymentExists
	}
	if err := in.checkPayable(amountCents); err != nil {
		return err
	}
	if !exists {
		p = &payment{seq: l.allocSeq()}
		l.payments[payID] = p
	}
	p.installment = installmentID
	p.at = at
	p.amount = amountCents
	p.revoked = false
	l.applyPayment(in, p)
	return nil
}

// CorrectPayment 更正还款的金额和/或生效时刻，不能改变所属分期。
func (l *Ledger) CorrectPayment(payID uint64, at, amountCents int64) error {
	p, ok := l.payments[payID]
	if !ok {
		return ErrPaymentNotFound
	}
	if p.revoked {
		return ErrPaymentRevoked
	}
	if amountCents <= 0 {
		return ErrInvalidAmount
	}
	in := l.installments[p.installment]
	if err := in.checkPayable(amountCents - p.amount); err != nil {
		return err
	}
	l.unapplyPayment(in, p)
	p.at = at
	p.amount = amountCents
	l.applyPayment(in, p)
	return nil
}

// RevokePayment 撤销还款，视为从未发生。重复撤销为无操作。
func (l *Ledger) RevokePayment(payID uint64) error {
	p, ok := l.payments[payID]
	if !ok {
		return ErrPaymentNotFound
	}
	if p.revoked {
		return nil
	}
	in := l.installments[p.installment]
	l.unapplyPayment(in, p)
	p.revoked = true
	return nil
}

// Query 返回买家在 at 时刻（含该时刻发生的还款与计罚）的历史汇总。
func (l *Ledger) Query(buyer uint64, at int64) Summary {
	bs, ok := l.buyers[buyer]
	if !ok {
		return Summary{}
	}
	pr := bs.events.prefix(at)
	return Summary{
		OutstandingCents: pr[compPrincipal] - pr[compPayment],
		PenaltyCents:     pr[compPenalty],
		Overdue15:        pr[compFlag] > 0,
	}
}

// checkPayable 校验再增加 delta 元还款后总额不超过本金。
func (in *installment) checkPayable(delta int64) error {
	var total int64
	if in.pays.root != nil {
		total = in.pays.root.sum[0]
	}
	if total+delta > in.principal {
		return ErrOverpaid
	}
	return nil
}

// applyPayment 把还款写入分期还款树与买家事件树，并重算派生数据。
func (l *Ledger) applyPayment(in *installment, p *payment) {
	in.pays.add(p.at, p.seq, [4]int64{p.amount, 0, 0, 0})
	l.buyers[in.buyer].events.add(p.at, p.seq, [4]int64{0, p.amount, 0, 0})
	l.recompute(in)
}

// unapplyPayment 把还款从分期还款树与买家事件树中移除，并重算派生数据。
func (l *Ledger) unapplyPayment(in *installment, p *payment) {
	in.pays.remove(p.at, p.seq)
	l.buyers[in.buyer].events.remove(p.at, p.seq)
	l.recompute(in)
}
