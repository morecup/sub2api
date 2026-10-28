package service

import "github.com/Wei-Shaw/sub2api/internal/config"

// ProvideOpenAIGatewayService installs egress resolution before background work starts.
func ProvideOpenAIGatewayService(
	accountRepo AccountRepository,
	usageLogRepo UsageLogRepository,
	usageBillingRepo UsageBillingRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	userGroupRateRepo UserGroupRateRepository,
	cache GatewayCache,
	cfg *config.Config,
	schedulerSnapshot *SchedulerSnapshotService,
	concurrencyService *ConcurrencyService,
	billingService *BillingService,
	rateLimitService *RateLimitService,
	billingCacheService *BillingCacheService,
	httpUpstream HTTPUpstream,
	deferredService *DeferredService,
	openAITokenProvider *OpenAITokenProvider,
	grokTokenProvider *GrokTokenProvider,
	resolver *ModelPricingResolver,
	channelService *ChannelService,
	balanceNotifyService *BalanceNotifyService,
	settingService *SettingService,
	userPlatformQuotaRepo UserPlatformQuotaRepository,
	tlsFPProfileService *TLSFingerprintProfileService,
	egressProfiles *CodexEgressProfileService,
) *OpenAIGatewayService {
	return NewOpenAIGatewayService(
		accountRepo,
		usageLogRepo,
		usageBillingRepo,
		userRepo,
		userSubRepo,
		userGroupRateRepo,
		cache,
		cfg,
		schedulerSnapshot,
		concurrencyService,
		billingService,
		rateLimitService,
		billingCacheService,
		httpUpstream,
		deferredService,
		openAITokenProvider,
		grokTokenProvider,
		resolver,
		channelService,
		balanceNotifyService,
		settingService,
		userPlatformQuotaRepo,
		tlsFPProfileService,
		egressProfiles,
	)
}

func ProvideCodexDesktopAPIService(factory PrivacyClientFactory, proxies ProxyRepository, profiles *CodexEgressProfileService) *CodexDesktopAPIService {
	s := NewCodexDesktopAPIService(factory, proxies)
	s.codexEgressProfiles = profiles
	return s
}
