package ai

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/HPZS/ai-form-backend/internal/credits"
	"github.com/HPZS/ai-form-backend/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type executionKey struct {
	userID                int64
	requestID, leaseToken string
}
type dispatchGuardKey struct{}

func cancellationRequested(db *gorm.DB, userID int64, requestID, taskID string) (bool, error) {
	var saved model.AIRequestCancellation
	err := db.Where("user_id = ? AND request_id = ?", userID, requestID).First(&saved).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if saved.TaskID != taskID {
		return false, credits.ErrConflict
	}
	return true, nil
}

// 每次上游分发都检查持久取消；通知丢失/跨进程时由有界观察补齐，不把 HTTP 断线当取消。
func (g *Gateway) executionContext(r *model.AIRequest) (context.Context, func()) {
	ctx, cancel := context.WithTimeout(context.Background(), CallChainTimeout)
	key := executionKey{r.UserID, r.RequestID, r.LeaseToken}
	g.executions.Store(key, context.CancelFunc(cancel))
	userID, requestID, taskID := r.UserID, r.RequestID, r.TaskID
	guard := func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		cancelled, err := cancellationRequested(g.db, userID, requestID, taskID)
		if err != nil {
			return err
		}
		if cancelled {
			cancel()
			return context.Canceled
		}
		return nil
	}
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := guard(); err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return
					}
					log.Printf("[AI-CANCEL-CHECK] request=%s err=%v", requestID, err)
				}
			}
		}
	}()
	return context.WithValue(ctx, dispatchGuardKey{}, guard), func() { cancel(); <-watchDone; g.executions.Delete(key) }
}

func checkDispatchContext(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if guard, ok := ctx.Value(dispatchGuardKey{}).(func() error); ok {
		return guard()
	}
	return nil
}

// CancelRequest 幂等记录意图；只有终态/未受理请求才能确认执行停止，202 不代表供应商停止计费。
func (g *Gateway) CancelRequest(c *gin.Context) {
	userID, requestID := c.GetInt64("userID"), c.Param("id")
	var body struct {
		TaskID string `json:"taskId"`
	}
	if _, err := uuid.Parse(requestID); err != nil {
		apiErr(c, 400, "INVALID_REQUEST", "requestId 必须是 UUID")
		return
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		apiErr(c, 400, "INVALID_REQUEST", "取消请求缺少任务身份")
		return
	}
	if body.TaskID != "" {
		if _, err := uuid.Parse(body.TaskID); err != nil {
			apiErr(c, 400, "INVALID_REQUEST", "taskId 必须是 UUID")
			return
		}
	}
	var request model.AIRequest
	found := false
	err := g.db.Transaction(func(tx *gorm.DB) error {
		if err := model.LockUser(tx, userID); err != nil {
			return err
		}
		err := tx.Where("user_id = ? AND request_id = ?", userID, requestID).First(&request).Error
		found = err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if found && request.TaskID != body.TaskID {
			return credits.ErrConflict
		}
		if found && request.Status != model.AIReqPending {
			return nil
		}
		if _, err := cancellationRequested(tx, userID, requestID, body.TaskID); err != nil {
			return err
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&model.AIRequestCancellation{UserID: userID, RequestID: requestID, TaskID: body.TaskID, CreatedAt: time.Now()}).Error
	})
	if err != nil {
		BillingError(c, err)
		return
	}
	if found && request.Status != model.AIReqPending {
		state := "completed"
		if request.FailureCode == "AI_CANCELLED" {
			state = "cancelled"
		}
		c.JSON(http.StatusOK, gin.H{"requestId": requestID, "executionStatus": state, "modelCalls": requestModelCalls(&request), "inputTokens": request.InputTokens, "outputTokens": request.OutputTokens, "usageComplete": requestUsageComplete(&request), "costStatus": "unknown"})
		return
	}
	g.executions.Range(func(key, value any) bool {
		identity := key.(executionKey)
		if identity.userID == userID && identity.requestID == requestID {
			value.(context.CancelFunc)()
		}
		return true
	})
	log.Printf("[AI-CANCEL-REQUESTED] user=%d request=%s admitted=%t", userID, requestID, found)
	status, state := http.StatusAccepted, "cancel-requested"
	if !found {
		status, state = http.StatusOK, "cancelled"
	}
	response := gin.H{"requestId": requestID, "executionStatus": state, "message": fmt.Sprintf("取消已登记，当前执行状态：%s；已发生用量保留", state)}
	if !found {
		response["modelCalls"], response["inputTokens"], response["outputTokens"], response["usageComplete"] = 0, 0, 0, true
	}
	c.JSON(status, response)
}
