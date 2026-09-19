package service

import (
	"sort"
	"strings"
	"time"
)

// grokWeeklyResetOrder uses the same weekly billing period as the UI's 7d bar.
// Missing, invalid or elapsed windows sort after known future resets. Never
// infer another week from stale data, or substitute a monthly/cooldown deadline.
func grokWeeklyResetOrder(account *Account, now time.Time) time.Time {
	if account == nil || account.Platform != PlatformGrok {
		return time.Time{}
	}
	billing, err := grokBillingSnapshotFromExtra(account.Extra)
	if err != nil || billing == nil || !strings.EqualFold(strings.TrimSpace(billing.PeriodType), "weekly") {
		return time.Time{}
	}
	reset, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(billing.PeriodEnd))
	if err != nil || !reset.After(now) {
		return time.Time{}
	}
	return reset.UTC()
}

func grokResetBefore(a, b time.Time) bool {
	return !a.IsZero() && (b.IsZero() || a.Before(b))
}

// Apply after load/LRU/weight ordering so those rules remain tie breakers.
// Only move Grok accounts within their existing priority slots; other platforms
// and manual priority retain their order, including in mixed candidate pools.
func preferGrokWeeklyReset[T any](items []T, accountOf func(T) *Account) {
	now := time.Now()
	groups := make(map[int][]int)
	resets := make(map[*Account]time.Time)
	for i, item := range items {
		account := accountOf(item)
		if account != nil && account.Platform == PlatformGrok {
			groups[account.Priority] = append(groups[account.Priority], i)
			resets[account] = grokWeeklyResetOrder(account, now)
		}
	}
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		ordered := make([]T, len(indices))
		for i, index := range indices {
			ordered[i] = items[index]
		}
		sort.SliceStable(ordered, func(i, j int) bool {
			return grokResetBefore(resets[accountOf(ordered[i])], resets[accountOf(ordered[j])])
		})
		for i, index := range indices {
			items[index] = ordered[i]
		}
	}
}

func schedulingAccount(account *Account) *Account { return account }

// Partition before top-K and weighted selection: a later reset must not win a
// lottery or remove an earlier reset from top-K. Keep every reset tier available
// for the normal runtime eligibility/concurrency checks and spillover.
func grokWeeklyCandidateGroups(pool []openAIAccountCandidateScore) [][]openAIAccountCandidateScore {
	type key struct {
		priority int
		reset    time.Time
	}
	now := time.Now()
	groups := make(map[key][]openAIAccountCandidateScore)
	keys := make([]key, 0)
	for _, candidate := range pool {
		k := key{candidate.account.Priority, grokWeeklyResetOrder(candidate.account, now)}
		if _, exists := groups[k]; !exists {
			keys = append(keys, k)
		}
		groups[k] = append(groups[k], candidate)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].priority != keys[j].priority {
			return keys[i].priority < keys[j].priority
		}
		return grokResetBefore(keys[i].reset, keys[j].reset)
	})
	result := make([][]openAIAccountCandidateScore, 0, len(keys))
	for _, k := range keys {
		result = append(result, groups[k])
	}
	return result
}
