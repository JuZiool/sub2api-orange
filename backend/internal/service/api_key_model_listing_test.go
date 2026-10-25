package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// ModelListingGroups 是聚合模型列表的入口：普通密钥只给主分组，聚合密钥给整条链。
func TestAPIKeyModelListingGroups(t *testing.T) {
	g1 := &Group{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}
	g2 := &Group{ID: 2, Platform: PlatformOpenAI, Status: StatusActive}
	g3 := &Group{ID: 3, Platform: PlatformOpenAI, Status: StatusActive}
	primary := int64(1)

	t.Run("普通密钥只返回主分组", func(t *testing.T) {
		key := &APIKey{GroupID: &primary, Group: g1}
		groups := key.ModelListingGroups()
		require.Len(t, groups, 1)
		require.Equal(t, int64(1), groups[0].ID)
	})

	t.Run("聚合密钥按优先级返回整条链", func(t *testing.T) {
		key := &APIKey{
			GroupID: &primary,
			Group:   g1,
			Groups: []*APIKeyGroupEntry{
				{Group: g1, Priority: 1},
				{Group: g3, Priority: 2},
				{Group: g2, Priority: 3},
			},
		}
		groups := key.ModelListingGroups()
		require.Len(t, groups, 3)
		require.Equal(t, []int64{1, 3, 2}, []int64{groups[0].ID, groups[1].ID, groups[2].ID},
			"必须保持 priority 顺序")
	})

	t.Run("链上重复分组只保留一次", func(t *testing.T) {
		key := &APIKey{
			GroupID: &primary,
			Group:   g1,
			Groups: []*APIKeyGroupEntry{
				{Group: g1, Priority: 1},
				{Group: g2, Priority: 2},
				{Group: g2, Priority: 3},
			},
		}
		require.Len(t, key.ModelListingGroups(), 2)
	})

	t.Run("未绑定分组返回空", func(t *testing.T) {
		require.Empty(t, (&APIKey{}).ModelListingGroups())
	})
}
