package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Orange 特有：详情读路径必须用 UserRepository 提供的完整授权列表再运行自愈。
type orangeChainOwnerReadAPIKeyRepoStub struct {
	APIKeyRepository
	key          *APIKey
	persistCalls int
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) GetByID(context.Context, int64) (*APIKey, error) {
	return r.key, nil
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) VerifyOwnership(_ context.Context, _ int64, ids []int64) ([]int64, error) {
	return ids, nil
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) OrangeReplaceGroupChain(context.Context, int64, []int64) error {
	r.persistCalls++
	return nil
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) OrangeReplaceGroupChainAndPrimary(context.Context, int64, int64, []int64, string) error {
	r.persistCalls++
	return nil
}

func (*orangeChainOwnerReadAPIKeyRepoStub) OrangeSetGroupChainMode(context.Context, int64, string) error {
	return nil
}

func (*orangeChainOwnerReadAPIKeyRepoStub) OrangeLoadGroupChains(context.Context, []int64) (map[int64][]OrangeGroupChainMember, error) {
	return nil, nil
}

func (*orangeChainOwnerReadAPIKeyRepoStub) OrangeLoadGroupChainModes(context.Context, []int64) (map[int64]string, error) {
	return nil, nil
}

func (*orangeChainOwnerReadAPIKeyRepoStub) OrangeListAPIKeyIDsByChainGroup(context.Context, int64) ([]int64, error) {
	return nil, nil
}

type orangeChainOwnerReadUserRepoStub struct {
	UserRepository
	user *User
	err  error
}

func (r *orangeChainOwnerReadUserRepoStub) GetByID(context.Context, int64) (*User, error) {
	return r.user, r.err
}

func TestOrangeGetAPIKeyGroupChainUsesOwnerAllowedGroups(t *testing.T) {
	const keyID = int64(983001)
	InvalidateOrangeGroupChainCache(keyID)
	defer InvalidateOrangeGroupChainCache(keyID)

	head := orangePolicyTestGroup(983011, PlatformAnthropic)
	exclusive := orangeEntitlementTestExclusiveGroup(983012)
	key := orangePolicyTestAPIKey(head)
	key.ID = keyID
	key.UserID = 7
	// Mirrors APIKeyRepository.GetByID: owner is loaded but AllowedGroups is not.
	key.User = orangeEntitlementTestUser()

	orangeGroupChainCache.mu.Lock()
	orangeGroupChainCache.entries[keyID] = orangeGroupChainCacheEntry{
		expiresAt: time.Now().Add(time.Minute),
		members:   []OrangeGroupChainMember{{GroupID: exclusive.ID, Priority: 1}},
		groups:    []*Group{exclusive},
		mode:      OrangeGroupChainModeFallback,
	}
	orangeGroupChainCache.mu.Unlock()

	keyRepo := &orangeChainOwnerReadAPIKeyRepoStub{key: key}
	owner := orangeEntitlementTestUser()
	owner.AllowedGroups = []int64{exclusive.ID}
	service := &APIKeyService{
		apiKeyRepo: keyRepo,
		userRepo:   &orangeChainOwnerReadUserRepoStub{user: owner},
	}

	view, err := service.OrangeGetAPIKeyGroupChain(context.Background(), owner.ID, keyID)
	if err != nil {
		t.Fatalf("OrangeGetAPIKeyGroupChain returned error: %v", err)
	}
	if len(view.Members) != 2 || view.Members[1].GroupID != exclusive.ID {
		t.Fatalf("granted exclusive group must remain in chain, got members %+v", view.Members)
	}
	if keyRepo.persistCalls != 0 {
		t.Fatalf("valid chain must not be rewritten, got %d persistence calls", keyRepo.persistCalls)
	}
}

func TestOrangeGetAPIKeyGroupChainDoesNotHealWhenOwnerPermissionsFail(t *testing.T) {
	const keyID = int64(983002)
	InvalidateOrangeGroupChainCache(keyID)
	defer InvalidateOrangeGroupChainCache(keyID)

	head := orangePolicyTestGroup(983021, PlatformAnthropic)
	key := orangePolicyTestAPIKey(head)
	key.ID = keyID
	key.UserID = 7
	key.User = orangeEntitlementTestUser()

	keyRepo := &orangeChainOwnerReadAPIKeyRepoStub{key: key}
	service := &APIKeyService{
		apiKeyRepo: keyRepo,
		userRepo: &orangeChainOwnerReadUserRepoStub{
			err: errors.New("user store unavailable"),
		},
	}

	if _, err := service.OrangeGetAPIKeyGroupChain(context.Background(), key.UserID, keyID); err == nil {
		t.Fatal("expected owner permission lookup error")
	}
	if keyRepo.persistCalls != 0 {
		t.Fatalf("must not persist self-heal without owner permissions, got %d calls", keyRepo.persistCalls)
	}
}
