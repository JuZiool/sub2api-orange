package service

func (s *OpenAIGatewayService) SetCodexQuotaOverdraftCoordinator(coordinator *CodexQuotaOverdraftCoordinator) {
	if s != nil {
		s.codexQuotaOverdraft = coordinator
	}
}

func (s *AccountUsageService) SetCodexQuotaOverdraftCoordinator(coordinator *CodexQuotaOverdraftCoordinator) {
	if s != nil {
		s.codexQuotaOverdraft = coordinator
	}
}

// codexQuotaOverdraftCoordinator returns the single coordinator owned by the
// OpenAI gateway. Keeping construction here avoids adding a fork-only provider
// to the generated Wire graph.
func (s *OpenAIGatewayService) codexQuotaOverdraftCoordinator(
	tlsFPProfileService *TLSFingerprintProfileService,
) *CodexQuotaOverdraftCoordinator {
	if s == nil {
		return nil
	}
	s.codexQuotaOverdraftOnce.Do(func() {
		if s.codexQuotaOverdraft != nil {
			return
		}
		var tempUnschedCache TempUnschedCache
		if s.rateLimitService != nil {
			tempUnschedCache = s.rateLimitService.tempUnschedCache
		}
		s.codexQuotaOverdraft = NewCodexQuotaOverdraftCoordinator(
			s.accountRepo,
			s.httpUpstream,
			s.openAITokenProvider,
			tlsFPProfileService,
			s.cfg,
			tempUnschedCache,
			s,
			s.rateLimitService,
		)
	})
	return s.codexQuotaOverdraft
}

// installCodexQuotaOverdraftSchedulingHooks 说明：Orange 把透支的阈值旁路与阻塞通知
// 直接内嵌到 RateLimitService（codexQuotaOverdraftBypassesSchedulingThreshold /
// notifyCodexQuotaOverdraftAwareSchedulingBlock），不再需要往上游函数体注入可选扩展点。
// 保留本函数作为显式接线入口，便于测试与后续维护时定位透支调度挂载点。
func installCodexQuotaOverdraftSchedulingHooks(_ *RateLimitService) {
	// Orange 特有：透支调度旁路已内嵌在 RateLimitService，无需额外注入。
}
