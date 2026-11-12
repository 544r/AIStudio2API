package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/Mag1cFall/AIStudio2API/internal/aistudio"
	"github.com/Mag1cFall/AIStudio2API/internal/api"
)

// Embed 返回 Build 通道移除后的固定错误：embedding 只能经 Build 代理调用，
// 而 Build 代理已随写代码助手通道一起移除。
func (service *trackedService) Embed(ctx context.Context, request aistudio.EmbeddingRequest) (aistudio.EmbeddingResult, error) {
	err := fmt.Errorf("%w: Build 通道已移除，/v1/embeddings 不可用", aistudio.ErrInvalidArgument)
	api.SetAccessLogError(ctx, err)
	return aistudio.EmbeddingResult{}, err
}

// embeddingCandidates 按已有 Worker 优先的顺序列出 Build 通道支持 embedding 模型的账户
func (service *trackedService) embeddingCandidates(ctx context.Context, modelID string) ([]string, error) {
	groups, err := service.pool.ClassifyCandidates(
		ctx, aistudio.AccountSelection{ModelID: modelID, Method: "embedContent"}, service.workers.WarmAccountIDs(),
	)
	if err != nil {
		return nil, err
	}
	standbyReady, readyBusyErr := service.workers.runtimeAvailable(groups.StandbyReady)
	standbyBusy, standbyBusyErr := service.workers.runtimeAvailable(groups.StandbyBusy)
	candidates := service.pool.OrderCandidates(append(append([]string(nil), groups.WarmReady...), groups.WarmAvailable...), modelID)
	candidates = append(candidates, service.pool.OrderCandidates(standbyReady, modelID)...)
	candidates = append(candidates, service.pool.OrderCandidates(groups.WarmBusy, modelID)...)
	candidates = append(candidates, service.pool.OrderCandidates(standbyBusy, modelID)...)
	if len(candidates) == 0 {
		if !groups.EarliestCooldown.Until.IsZero() {
			return nil, &aistudio.AllCoolingError{ModelID: modelID, Until: groups.EarliestCooldown.Until, Reason: groups.EarliestCooldown.Reason}
		}
		if busyErr := errors.Join(readyBusyErr, standbyBusyErr); busyErr != nil {
			return nil, busyErr
		}
		return nil, aistudio.ErrNoEligibleAccount
	}
	return candidates, nil
}

// Embed 由当前生成服务执行 embedding
func (manager *runtimeManager) Embed(ctx context.Context, request aistudio.EmbeddingRequest) (aistudio.EmbeddingResult, error) {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.current.service.(aistudio.EmbeddingService).Embed(ctx, request)
}

// DoProtectedBuild 在认证失败后续签同一账户并重放 Build 代理请求
func (transport *authRetryProtectedTransport) DoProtectedBuild(
	ctx context.Context,
	selection aistudio.AccountSelection,
	page string,
	rpc aistudio.RPCRequest,
) (*aistudio.RPCResponse, error) {
	return transport.refresher.do(ctx, rpc.Method, func() (*aistudio.RPCResponse, error) {
		return transport.transport.(aistudio.BuildProtectedTransport).DoProtectedBuild(ctx, selection, page, rpc)
	})
}

var _ aistudio.EmbeddingService = (*trackedService)(nil)
var _ aistudio.EmbeddingService = (*runtimeManager)(nil)
var _ aistudio.BuildProtectedTransport = (*authRetryProtectedTransport)(nil)
