// Orange 特有：分组链列表 DTO 的映射测试（契约 B）。
//
// 列表 groups[] 的元素必须与链详情 members[] 完全一致：分组已被删除的成员（state=missing）
// 也要输出一行 { group_id, priority, state }，此时 group 字段为空（omitempty）。
package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/stretchr/testify/require"
)

func TestOrangeAPIKeyGroupEntriesFromServiceKeepsMissingMember(t *testing.T) {
	head := &service.Group{ID: 2, Name: "主分组", Platform: "anthropic", Status: service.StatusActive}
	entries := []*service.APIKeyGroupEntry{
		{GroupID: 2, Group: head, Priority: 1, State: service.OrangeChainMemberStateAvailable},
		// 已删除的分组：没有 *Group，只有 ID（列表里也必须占一行）。
		{GroupID: 18, Priority: 2, State: service.OrangeChainMemberStateMissing},
	}

	out := apiKeyGroupEntriesFromService(entries)
	require.Len(t, out, 2)
	require.Equal(t, int64(2), out[0].GroupID)
	require.Equal(t, "available", out[0].State)
	require.NotNil(t, out[0].Group)
	require.Equal(t, int64(18), out[1].GroupID)
	require.Equal(t, 2, out[1].Priority)
	require.Equal(t, "missing", out[1].State)
	require.Nil(t, out[1].Group)

	// 序列化后 missing 成员只有 group_id / priority / state 三个字段（group 被省略）。
	raw, err := json.Marshal(out[1])
	require.NoError(t, err)
	require.JSONEq(t, `{"group_id":18,"priority":2,"state":"missing"}`, string(raw))
}

func TestOrangeAPIKeyGroupEntriesFromServiceEmpty(t *testing.T) {
	require.Equal(t, []APIKeyGroupEntry{}, apiKeyGroupEntriesFromService(nil))
}
