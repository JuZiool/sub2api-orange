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
	chainMembers []OrangeGroupChainMember
	chainMode    string
	persistCalls int
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) GetByID(context.Context, int64) (*APIKey, error) {
	return r.key, nil
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) VerifyOwnership(_ context.Context, _ int64, ids []int64) ([]int64, error) {
	return ids, nil
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) OrangeReplaceGroupChain(_ context.Context, _ int64, _ []int64) error {
	r.persistCalls++
	r.chainMembers = nil
	r.chainMode = OrangeGroupChainModeNone
	return nil
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) OrangeReplaceGroupChainAndPrimary(_ context.Context, _ int64, primaryGroupID int64, tail []int64, mode string) error {
	r.persistCalls++
	if r.key != nil {
		r.key.GroupID = &primaryGroupID
	}
	r.chainMembers = make([]OrangeGroupChainMember, len(tail))
	for i, groupID := range tail {
		r.chainMembers[i] = OrangeGroupChainMember{GroupID: groupID, Priority: i + 1}
	}
	r.chainMode = mode
	return nil
}

func (*orangeChainOwnerReadAPIKeyRepoStub) OrangeSetGroupChainMode(context.Context, int64, string) error {
	return nil
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) OrangeLoadGroupChains(_ context.Context, ids []int64) (map[int64][]OrangeGroupChainMember, error) {
	chains := make(map[int64][]OrangeGroupChainMember, len(ids))
	for _, id := range ids {
		if r.key != nil && id == r.key.ID {
			chains[id] = append([]OrangeGroupChainMember(nil), r.chainMembers...)
		}
	}
	return chains, nil
}

func (r *orangeChainOwnerReadAPIKeyRepoStub) OrangeLoadGroupChainModes(_ context.Context, ids []int64) (map[int64]string, error) {
	modes := make(map[int64]string, len(ids))
	for _, id := range ids {
		if r.key != nil && id == r.key.ID {
			modes[id] = r.chainMode
		}
	}
	return modes, nil
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

type orangeChainOwnerReadGroupRepoStub struct {
	GroupRepository
	groups map[int64]*Group
}

func (r *orangeChainOwnerReadGroupRepoStub) GetByID(_ context.Context, id int64) (*Group, error) {
	group := r.groups[id]
	if group == nil {
		return nil, ErrGroupNotFound
	}
	return group, nil
}

func (r *orangeChainOwnerReadGroupRepoStub) GetByIDLite(ctx context.Context, id int64) (*Group, error) {
	return r.GetByID(ctx, id)
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

func TestOrangeSetAPIKeyGroupChainReturnsAuthorizedExclusiveGroups(t *testing.T) {
	const keyID = int64(983003)
	InvalidateOrangeGroupChainCache(keyID)
	defer InvalidateOrangeGroupChainCache(keyID)

	head := orangePolicyTestGroup(983031, PlatformAnthropic)
	exclusive := orangeEntitlementTestExclusiveGroup(983032)
	key := orangePolicyTestAPIKey(head)
	key.ID = keyID
	key.UserID = 7
	// Mirrors APIKeyRepository.GetByID: the attached User lacks the AllowedGroups edge.
	key.User = orangeEntitlementTestUser()

	owner := orangeEntitlementTestUser()
	owner.AllowedGroups = []int64{exclusive.ID}
	keyRepo := &orangeChainOwnerReadAPIKeyRepoStub{key: key}
	service := &APIKeyService{
		apiKeyRepo: keyRepo,
		userRepo:   &orangeChainOwnerReadUserRepoStub{user: owner},
		groupRepo: &orangeChainOwnerReadGroupRepoStub{groups: map[int64]*Group{
			head.ID: head, exclusive.ID: exclusive,
		}},
	}

	view, err := service.OrangeSetAPIKeyGroupChain(context.Background(), owner.ID, keyID,
		[]int64{head.ID, exclusive.ID}, OrangeGroupChainModeFallback)
	if err != nil {
		t.Fatalf("OrangeSetAPIKeyGroupChain returned error: %v", err)
	}
	if len(view.Members) != 2 || view.Members[1].GroupID != exclusive.ID {
		t.Fatalf("saved authorized exclusive group must be present in response, got members %+v", view.Members)
	}
	if keyRepo.persistCalls != 1 {
		t.Fatalf("expected one chain write, got %d", keyRepo.persistCalls)
	}
}

func TestOrangeSetAPIKeyGroupChainDoesNotWriteWhenOwnerPermissionsFail(t *testing.T) {
	const keyID = int64(983004)
	InvalidateOrangeGroupChainCache(keyID)
	defer InvalidateOrangeGroupChainCache(keyID)

	head := orangePolicyTestGroup(983041, PlatformAnthropic)
	key := orangePolicyTestAPIKey(head)
	key.ID = keyID
	key.UserID = 7
	keyRepo := &orangeChainOwnerReadAPIKeyRepoStub{key: key}
	service := &APIKeyService{
		apiKeyRepo: keyRepo,
		userRepo: &orangeChainOwnerReadUserRepoStub{
			err: errors.New("user store unavailable"),
		},
	}

	if _, err := service.OrangeSetAPIKeyGroupChain(context.Background(), key.UserID, keyID, nil, OrangeGroupChainModeFallback); err == nil {
		t.Fatal("expected owner permission lookup error")
	}
	if keyRepo.persistCalls != 0 {
		t.Fatalf("must not write chain without owner permissions, got %d calls", keyRepo.persistCalls)
	}
}
