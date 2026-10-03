// Package credit 提供赊账分期历史核算能力：登记分期、写入/更正/撤销还款，
// 并依据最新事实在任意历史时刻查询买家的未偿本金、累计应计罚金与逾期标记。
// 数据仅保存在进程内存，调用需串行执行。
package credit

import "errors"

// SecondsPerDay 一天固定的秒数。
const SecondsPerDay = 86400

// 罚金规则常量。
const (
	PenaltyIntervalDays = 7  // 每隔 7 天计罚一次
	PenaltyMaxTimes     = 8  // 最多计罚 8 次（第 56 天后不再增加）
	PenaltyRateNum      = 15 // 每次按未偿本金的 1.5% 计罚
	PenaltyRateDen      = 1000
	PenaltyCapNum       = 12 // 累计罚金上限为原本金的 12%
	PenaltyCapDen       = 100
	OverdueDays         = 15 // 逾期至少 15 天
)

// 业务错误。
var (
	ErrInstallmentExists   = errors.New("credit: installment already exists")
	ErrInstallmentNotFound = errors.New("credit: installment not found")
	ErrPaymentExists       = errors.New("credit: payment already exists")
	ErrPaymentNotFound     = errors.New("credit: payment not found")
	ErrPaymentRevoked      = errors.New("credit: payment revoked")
	ErrInvalidAmount       = errors.New("credit: invalid amount")
	ErrOverpaid            = errors.New("credit: payments exceed principal")
)

// Summary 是买家在某一历史时刻的汇总结果。
type Summary struct {
	// OutstandingCents 未偿本金总额（分），不含尚未生效的分期。
	OutstandingCents int64
	// PenaltyCents 累计应计罚金（分），本金结清后仍保留。
	PenaltyCents int64
	// Overdue15 是否存在本金未清且逾期至少 15 天的分期。
	Overdue15 bool
}
