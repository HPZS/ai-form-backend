package credits

import (
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

// TaskTotals 的消费跨授权累计，返还不重新打开自动消费预算。
type TaskTotals struct {
	Charged  int64 `json:"charged"`
	Refunded int64 `json:"refunded"`
	Net      int64 `json:"net"`
	InFlight int64 `json:"inFlight"`
	Pending  int64 `json:"pending"`
}

func Totals(db *gorm.DB, userID int64, taskID string) (TaskTotals, error) {
	var t TaskTotals
	var requests []model.AIRequest
	if err := db.Where("user_id = ? AND task_id = ?", userID, taskID).Find(&requests).Error; err != nil {
		return t, err
	}
	ids := make([]string, 0, len(requests))
	for _, r := range requests {
		t.Charged += r.Credits
		t.InFlight += r.ReservedCredits
		if r.PolicyVersion == PolicyV2 && (r.Status == model.AIReqPending || r.Status == SettlementPending) {
			t.Pending++
		}
		ids = append(ids, r.RequestID)
	}
	if len(ids) > 0 {
		if err := db.Model(&model.CreditLedger{}).Where("user_id = ? AND refund_of_request_id IN ? AND delta > 0", userID, ids).Select("COALESCE(SUM(delta), 0)").Scan(&t.Refunded).Error; err != nil {
			return t, err
		}
	}
	t.Net = t.Charged - t.Refunded
	return t, nil
}

// reservedByBucket 汇总任务未分配资金和请求已分配资金，转移时不重复冻结。
func reservedByBucket(tx *gorm.DB, userID int64, now time.Time) (map[int64]int64, error) {
	totals := map[int64]int64{}
	var holds []model.CreditHold
	if err := tx.Where("user_id = ? AND policy_version = ? AND status = ? AND expires_at > ?", userID, PolicyV2, model.HoldStatusOpen, now).Find(&holds).Error; err != nil {
		return nil, err
	}
	for _, h := range holds {
		for _, a := range h.Allocations {
			totals[a.BucketID] += a.Amount
		}
	}
	var requests []model.AIRequest
	if err := tx.Where("user_id = ? AND policy_version = ? AND reserved_credits > 0", userID, PolicyV2).Find(&requests).Error; err != nil {
		return nil, err
	}
	for _, r := range requests {
		for _, a := range r.Allocations {
			totals[a.BucketID] += a.Amount
		}
	}
	return totals, nil
}

func V2Reserved(tx *gorm.DB, userID int64, now time.Time) (int64, error) {
	byBucket, err := reservedByBucket(tx, userID, now)
	if err != nil {
		return 0, err
	}
	subs, err := activeBuckets(tx, userID, now)
	if err != nil {
		return 0, err
	}
	var sum int64
	for _, b := range subs {
		sum += byBucket[b.ID]
	}
	return sum, nil
}

func allocate(tx *gorm.DB, userID, amount int64) ([]model.BucketAllocation, error) {
	now := time.Now()
	subs, err := activeBuckets(tx, userID, now)
	if err != nil {
		return nil, err
	}
	reserved, err := reservedByBucket(tx, userID, now)
	if err != nil {
		return nil, err
	}
	// 旧在途任务的无分桶预占仍受保护，不能被新规则花掉。
	legacy, err := legacyReserved(tx, userID, now, "")
	if err != nil {
		return nil, err
	}
	out := []model.BucketAllocation{}
	for _, b := range subs {
		free := max(int64(0), b.AmountTotal-b.AmountUsed-reserved[b.ID])
		protected := min(free, legacy)
		free -= protected
		legacy -= protected
		take := min(free, amount)
		if take > 0 {
			out = append(out, model.BucketAllocation{BucketID: b.ID, Amount: take})
			amount -= take
		}
	}
	if amount > 0 {
		return nil, ErrInsufficient
	}
	return out, nil
}

// Quote 保留旧插件的两项计数契约，新客户端使用任意已配置能力的 calls。
func Quote(db *gorm.DB, userID int64, taskID string, matchCalls, generateCalls int64) (*model.CreditHold, error) {
	return QuoteCapabilities(db, userID, taskID, map[string]int64{"match_columns": matchCalls, "generate_field": generateCalls})
}

func QuoteCapabilities(db *gorm.DB, userID int64, taskID string, calls map[string]int64) (*model.CreditHold, error) {
	if _, err := uuid.Parse(taskID); err != nil {
		return nil, ErrAuthorization
	}
	prices, err := Prices(db)
	if err != nil {
		return nil, err
	}
	var estimated int64
	for capability, count := range calls {
		if count < 0 || count > 1_000_000 {
			return nil, fmt.Errorf("预计次数不合法")
		}
		price, ok := prices[capability]
		if !ok {
			return nil, fmt.Errorf("%w: %s", ErrConfig, capability)
		}
		estimated += count * price.Credits
	}
	totals, err := Totals(db, userID, taskID)
	if err != nil {
		return nil, err
	}
	if totals.Charged+totals.InFlight+estimated > 1_000_000_000_000 {
		return nil, ErrBudget
	}
	q := model.CreditHold{ID: uuid.NewString(), UserID: userID, TaskID: taskID, PolicyVersion: PolicyV2, Status: "quoted", Prices: prices,
		Amount: totals.Charged + totals.InFlight + estimated, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(10 * time.Minute)}
	if err := db.Create(&q).Error; err != nil {
		return nil, err
	}
	return &q, nil
}

// Confirm 原子替换本任务上一授权，保留原请求及其资金；上限始终是任务总额。
func Confirm(db *gorm.DB, userID int64, quoteID string, maxTotal int64) (*model.CreditHold, error) {
	var h model.CreditHold
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, userID); err != nil {
			return err
		}
		if err := tx.Where("id = ? AND user_id = ? AND policy_version = ?", quoteID, userID, PolicyV2).First(&h).Error; err != nil {
			return err
		}
		if h.Status == model.HoldStatusOpen {
			if h.Amount != maxTotal {
				return ErrConflict
			}
			if time.Now().After(h.ExpiresAt) {
				return ErrQuoteExpired
			}
			return nil
		}
		if h.Status != "quoted" || time.Now().After(h.ExpiresAt) {
			return ErrQuoteExpired
		}
		prices, err := Prices(tx)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(prices, h.Prices) {
			return ErrQuoteExpired
		}
		sub, err := HasSubscription(tx, userID)
		if err != nil {
			return err
		}
		if !sub {
			return ErrNoActiveSub
		}
		totals, err := Totals(tx, userID, h.TaskID)
		if err != nil {
			return err
		}
		if maxTotal < totals.Charged+totals.InFlight || maxTotal < h.Amount || maxTotal > 1_000_000_000_000 {
			return ErrBudget
		}
		if err := tx.Model(&model.CreditHold{}).Where("user_id = ? AND task_id = ? AND status = ?", userID, h.TaskID, model.HoldStatusOpen).Updates(map[string]any{"status": model.HoldStatusSettled, "settled_at": time.Now()}).Error; err != nil {
			return err
		}
		allocation, err := allocate(tx, userID, maxTotal-totals.Charged-totals.InFlight)
		if err != nil {
			return err
		}
		expires := time.Now().Add(24 * time.Hour)
		h.Amount, h.Status, h.Allocations = maxTotal, model.HoldStatusOpen, allocation
		h.ExpiresAt, h.PriceExpiresAt = time.Now().Add(2*time.Hour), &expires
		return tx.Save(&h).Error
	})
	return &h, err
}

// ReserveRequest 由网关在用户锁内调用，给尚未创建的请求分配已经授权的资金。
func ReserveRequest(tx *gorm.DB, r *model.AIRequest) error {
	var h model.CreditHold
	err := tx.Where("id = ? AND user_id = ? AND task_id = ? AND policy_version = ?", r.AuthorizationVersion, r.UserID, r.TaskID, PolicyV2).First(&h).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrAuthorization
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if h.Status != model.HoldStatusOpen || !now.Before(h.ExpiresAt) {
		return ErrAuthorization
	}
	if h.PriceExpiresAt == nil || !now.Before(*h.PriceExpiresAt) {
		return ErrQuoteExpired
	}
	price, ok := h.Prices[r.Capability]
	if !ok || price.Credits <= 0 {
		return ErrQuoteExpired
	}
	// 只在接受新请求时核对当前价格；已接受/重放请求仍按原请求快照结算。
	var current model.CapabilityPrice
	if err := tx.Where("capability = ?", r.Capability).First(&current).Error; err != nil {
		return err
	}
	if !ValidPrice(current) {
		return ErrConfig
	}
	if current.BillingMode != ModePerCall || price.Version != current.PriceVersion || price.Credits != current.Credits {
		return ErrQuoteExpired
	}
	r.UnitPrice, r.PriceVersion = price.Credits, price.Version
	totals, err := Totals(tx, r.UserID, r.TaskID)
	if err != nil {
		return err
	}
	if totals.Charged+totals.InFlight+r.UnitPrice > h.Amount {
		return ErrBudget
	}
	var subs []model.UserSubscription
	if err := tx.Where("user_id = ? AND status = ? AND starts_at <= ? AND ends_at > ?", r.UserID, model.SubStatusActive, now, now).Find(&subs).Error; err != nil {
		return err
	}
	active := map[int64]bool{}
	for _, b := range subs {
		active[b.ID] = true
	}
	remaining := r.UnitPrice
	for i := range h.Allocations {
		a := &h.Allocations[i]
		if !active[a.BucketID] {
			a.Amount = 0
			continue
		}
		take := min(a.Amount, remaining)
		if take > 0 {
			r.Allocations = append(r.Allocations, model.BucketAllocation{BucketID: a.BucketID, Amount: take})
			a.Amount -= take
			remaining -= take
		}
	}
	if remaining > 0 {
		return ErrInsufficient
	}
	r.ReservedCredits = r.UnitPrice
	return tx.Save(&h).Error
}

// ReleaseRequest 失败请求返还给原有效任务池；关闭/过期任务只释放，不恢复授权。
func ReleaseRequest(tx *gorm.DB, r *model.AIRequest) error {
	if r.ReservedCredits == 0 {
		return nil
	}
	var h model.CreditHold
	err := tx.Where("id = ? AND user_id = ?", r.AuthorizationVersion, r.UserID).First(&h).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if err == nil && h.Status == model.HoldStatusOpen && time.Now().Before(h.ExpiresAt) {
		for _, a := range r.Allocations {
			found := false
			for i := range h.Allocations {
				if h.Allocations[i].BucketID == a.BucketID {
					h.Allocations[i].Amount += a.Amount
					found = true
					break
				}
			}
			if !found {
				h.Allocations = append(h.Allocations, a)
			}
		}
		if err := tx.Save(&h).Error; err != nil {
			return err
		}
	}
	r.ReservedCredits, r.Allocations = 0, nil
	return nil
}

// DebitReserved 在网关结算事务内执行；自然到期不撤销已接受请求的资金承诺。
func DebitReserved(tx *gorm.DB, r *model.AIRequest) error {
	if r.UnitPrice == 0 {
		return ReleaseRequest(tx, r)
	}
	if r.ReservedCredits != r.UnitPrice {
		return ErrFundsRevoked
	}
	var total int64
	for _, a := range r.Allocations {
		if a.Amount <= 0 {
			return ErrFundsRevoked
		}
		var b model.UserSubscription
		if err := tx.Where("id = ? AND user_id = ?", a.BucketID, r.UserID).First(&b).Error; err != nil {
			return err
		}
		if b.Status == model.SubStatusRevoked || b.AmountTotal-b.AmountUsed < a.Amount {
			return ErrFundsRevoked
		}
		if err := tx.Model(&b).Update("amount_used", gorm.Expr("amount_used + ?", a.Amount)).Error; err != nil {
			return err
		}
		row := model.CreditLedger{UserID: r.UserID, TaskID: r.TaskID, RequestID: r.RequestID, SubscriptionID: a.BucketID, Capability: r.Capability,
			PolicyVersion: PolicyV2, PriceVersion: r.PriceVersion, PriceSnapshot: r.UnitPrice, Delta: -a.Amount, Reason: "successful_call", CreatedAt: time.Now()}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		total += a.Amount
	}
	if total != r.UnitPrice {
		return ErrFundsRevoked
	}
	if err := tx.Model(&model.CreditHold{}).Where("id = ? AND user_id = ?", r.AuthorizationVersion, r.UserID).Update("consumed", gorm.Expr("consumed + ?", total)).Error; err != nil {
		return err
	}
	r.Credits, r.ReservedCredits, r.Allocations = total, 0, nil
	return nil
}
