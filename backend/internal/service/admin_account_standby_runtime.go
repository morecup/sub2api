package service

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// enrichStandbyRuntimeStates 批量补齐管理端需要的备用账号实时状态。
// 所有关联主账号通过一次 GetByIDs 加载，避免账号列表产生 N+1 查询。
func (s *adminServiceImpl) enrichStandbyRuntimeStates(ctx context.Context, accounts []*Account) error {
	if len(accounts) == 0 {
		return nil
	}

	primaryIDSet := make(map[int64]struct{})
	for _, account := range accounts {
		if account == nil || !account.HasStandbyConfiguration() || account.StandbyForAccountID == nil {
			continue
		}
		if primaryID := *account.StandbyForAccountID; primaryID > 0 {
			primaryIDSet[primaryID] = struct{}{}
		}
	}

	evaluationCtx := ctx
	if s != nil && s.settingService != nil {
		evaluationCtx = withOpenAIQuotaAutoPauseSettings(
			ctx,
			s.settingService.GetOpenAIQuotaAutoPauseSettings(ctx),
		)
	}

	primariesByID := make(map[int64]*Account, len(primaryIDSet))
	if len(primaryIDSet) > 0 {
		if s == nil || s.accountRepo == nil {
			return fmt.Errorf("standby runtime state: account repository is unavailable")
		}
		primaryIDs := make([]int64, 0, len(primaryIDSet))
		for primaryID := range primaryIDSet {
			primaryIDs = append(primaryIDs, primaryID)
		}
		sort.Slice(primaryIDs, func(i, j int) bool { return primaryIDs[i] < primaryIDs[j] })

		primaries, err := s.accountRepo.GetByIDs(ctx, primaryIDs)
		if err != nil {
			return fmt.Errorf("load standby primary accounts: %w", err)
		}
		for _, primary := range primaries {
			if primary != nil {
				primariesByID[primary.ID] = primary
			}
		}
	}

	now := time.Now()
	for _, account := range accounts {
		var primary *Account
		if account != nil && account.StandbyForAccountID != nil {
			primary = primariesByID[*account.StandbyForAccountID]
		}
		populateStandbyRuntimeState(evaluationCtx, account, primary, now)
	}
	return nil
}

func accountPointers(accounts []Account) []*Account {
	result := make([]*Account, len(accounts))
	for i := range accounts {
		result[i] = &accounts[i]
	}
	return result
}
