package service

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// StandbyTriggerType 表示备用账号自动接管的触发条件。
// 同一备用账号配置的多个条件按 OR 关系计算。
type StandbyTriggerType string

// StandbyRuntimeState 表示备用账号在当前时刻的实际运行状态。
type StandbyRuntimeState string

const (
	StandbyTriggerQuota5hExhausted  StandbyTriggerType = "quota_5h_exhausted"
	StandbyTriggerQuota7dExhausted  StandbyTriggerType = "quota_7d_exhausted"
	StandbyTriggerRateLimited       StandbyTriggerType = "rate_limited"
	StandbyTriggerQuotaExhausted    StandbyTriggerType = "quota_exhausted"
	StandbyTriggerAccountExpired    StandbyTriggerType = "account_expired"
	StandbyTriggerAccountError      StandbyTriggerType = "account_error"
	StandbyTriggerTempUnschedulable StandbyTriggerType = "temp_unschedulable"
	StandbyTriggerManualDisabled    StandbyTriggerType = "manual_disabled"

	StandbyRuntimeStateWaiting     StandbyRuntimeState = "waiting"
	StandbyRuntimeStateActive      StandbyRuntimeState = "active"
	StandbyRuntimeStateUnavailable StandbyRuntimeState = "unavailable"
	StandbyRuntimeStateInvalid     StandbyRuntimeState = "invalid"
)

var validStandbyTriggerTypes = map[StandbyTriggerType]struct{}{
	StandbyTriggerQuota5hExhausted:  {},
	StandbyTriggerQuota7dExhausted:  {},
	StandbyTriggerRateLimited:       {},
	StandbyTriggerQuotaExhausted:    {},
	StandbyTriggerAccountExpired:    {},
	StandbyTriggerAccountError:      {},
	StandbyTriggerTempUnschedulable: {},
	StandbyTriggerManualDisabled:    {},
}

// NormalizeStandbyTriggerTypes 校验、去重并规范化接管条件。
func NormalizeStandbyTriggerTypes(values []string) ([]string, error) {
	if len(values) == 0 {
		return []string{}, nil
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[StandbyTriggerType]struct{}, len(values))
	for _, raw := range values {
		trigger := StandbyTriggerType(strings.ToLower(strings.TrimSpace(raw)))
		if _, ok := validStandbyTriggerTypes[trigger]; !ok {
			return nil, fmt.Errorf("unsupported standby trigger type %q", raw)
		}
		if _, duplicate := seen[trigger]; duplicate {
			continue
		}
		seen[trigger] = struct{}{}
		normalized = append(normalized, string(trigger))
	}
	return normalized, nil
}

// HasStandbyConfiguration 报告账号是否带有任何备用接管配置。
// 目标账号被删除后外键会置空但触发条件仍保留，此时返回 true，让调度 fail closed。
func (a *Account) HasStandbyConfiguration() bool {
	return a != nil && (a.StandbyForAccountID != nil || len(a.StandbyTriggerTypes) > 0)
}

// IsStandby 报告账号是否具有结构完整的备用接管配置。
func (a *Account) IsStandby() bool {
	return a != nil && a.StandbyForAccountID != nil && *a.StandbyForAccountID > 0 && len(a.StandbyTriggerTypes) > 0
}

// StandbyActivationResult 是一次主账号状态判定的结果。
type StandbyActivationResult struct {
	Active          bool
	MatchedTriggers []string
}

// EvaluateStandbyActivation 计算备用账号是否应接管。
// 多个条件按 OR 生效；返回全部已命中的条件，便于测试和后续诊断展示。
func EvaluateStandbyActivation(ctx context.Context, standby, primary *Account, now time.Time) StandbyActivationResult {
	result := StandbyActivationResult{MatchedTriggers: []string{}}
	if standby == nil || primary == nil || !standby.IsStandby() {
		return result
	}
	if standby.ID <= 0 || primary.ID <= 0 || standby.ID == primary.ID || standby.Platform != primary.Platform {
		return result
	}
	if standby.StandbyForAccountID == nil || *standby.StandbyForAccountID != primary.ID {
		return result
	}

	threshold5h, threshold7d := resolveOpenAIQuotaAutoPauseThresholds(ctx, primary)
	if threshold5h <= 0 {
		threshold5h = 1
	}
	if threshold7d <= 0 {
		threshold7d = 1
	}

	for _, raw := range standby.StandbyTriggerTypes {
		trigger := StandbyTriggerType(raw)
		matched := false
		switch trigger {
		case StandbyTriggerQuota5hExhausted:
			utilization, ok := resolveOpenAIQuotaUtilization(primary.Extra, "5h", now)
			matched = ok && utilization >= threshold5h
		case StandbyTriggerQuota7dExhausted:
			utilization, ok := resolveOpenAIQuotaUtilization(primary.Extra, "7d", now)
			matched = ok && utilization >= threshold7d
		case StandbyTriggerRateLimited:
			matched = primary.RateLimitResetAt != nil && now.Before(*primary.RateLimitResetAt)
		case StandbyTriggerQuotaExhausted:
			matched = primary.IsQuotaExceeded()
			if !matched && primary.IsGrok() {
				matched, _ = shouldAutoPauseGrokAccountByQuotaAt(primary, now)
			}
		case StandbyTriggerAccountExpired:
			matched = primary.ExpiresAt != nil && !now.Before(*primary.ExpiresAt)
		case StandbyTriggerAccountError:
			matched = primary.Status == StatusError
		case StandbyTriggerTempUnschedulable:
			matched = primary.TempUnschedulableUntil != nil && now.Before(*primary.TempUnschedulableUntil)
		case StandbyTriggerManualDisabled:
			matched = primary.Status != StatusActive || !primary.Schedulable
		default:
			// 非法或未来版本条件一律不触发；管理 API 会阻止新写入非法值。
			continue
		}
		if matched {
			result.MatchedTriggers = append(result.MatchedTriggers, string(trigger))
		}
	}
	result.Active = len(result.MatchedTriggers) > 0
	return result
}

// populateStandbyRuntimeState 将一次主备判定结果写入仅供管理端展示的运行态字段。
// 配置异常时保持 fail closed；配置有效时，即使备用账号自身不可调度，也会保留
// 已命中的条件，便于管理端诊断为什么本应接管却没有进入候选池。
func populateStandbyRuntimeState(ctx context.Context, standby, primary *Account, now time.Time) {
	if standby == nil {
		return
	}

	standby.StandbyRuntimeState = ""
	standby.StandbyPrimaryName = ""
	standby.StandbyMatchedTriggerTypes = nil
	if !standby.HasStandbyConfiguration() {
		return
	}

	standby.StandbyRuntimeState = StandbyRuntimeStateInvalid
	normalizedTriggers, err := NormalizeStandbyTriggerTypes(standby.StandbyTriggerTypes)
	if err != nil || standby.StandbyForAccountID == nil || *standby.StandbyForAccountID <= 0 || len(normalizedTriggers) == 0 {
		return
	}
	if primary == nil {
		return
	}

	standby.StandbyPrimaryName = primary.Name
	if standby.ID <= 0 || primary.ID <= 0 || standby.ID == primary.ID ||
		standby.Platform != primary.Platform || *standby.StandbyForAccountID != primary.ID {
		return
	}

	// 使用规范化后的条件计算，兼容历史数据中的大小写或首尾空格，同时不修改持久化字段。
	evaluationStandby := *standby
	evaluationStandby.StandbyTriggerTypes = normalizedTriggers
	result := EvaluateStandbyActivation(ctx, &evaluationStandby, primary, now)
	standby.StandbyMatchedTriggerTypes = append([]string{}, result.MatchedTriggers...)

	if !standby.isSchedulableAt(now) {
		standby.StandbyRuntimeState = StandbyRuntimeStateUnavailable
		return
	}
	if result.Active {
		standby.StandbyRuntimeState = StandbyRuntimeStateActive
		return
	}
	standby.StandbyRuntimeState = StandbyRuntimeStateWaiting
}

// accountSchedulableWithStandby 在账号自身可调度性之上增加备用接管门控。
// 未配置接管的普通账号保持原行为；配置残缺、主账号不存在或读取失败时 fail closed。
func accountSchedulableWithStandby(
	ctx context.Context,
	account *Account,
	lookupPrimary func(context.Context, int64) (*Account, error),
) bool {
	if account == nil || !account.IsSchedulable() {
		return false
	}
	if !account.HasStandbyConfiguration() {
		return true
	}
	if !account.IsStandby() || lookupPrimary == nil {
		return false
	}
	primary, err := lookupPrimary(ctx, *account.StandbyForAccountID)
	if err != nil || primary == nil {
		return false
	}
	return EvaluateStandbyActivation(ctx, account, primary, time.Now()).Active
}
