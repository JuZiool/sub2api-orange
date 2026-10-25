package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// APIKeyGroup 是 Orange 特有的聚合密钥分组优先级链。
//
// 一个 API Key 可以按 priority 升序依次尝试多个分组：priority=1 即主分组
// （同时写入 api_keys.group_id，保证既有鉴权/路由/计费路径无需感知本表），
// 后续条目在主分组无可用账号时按序兜底。
type APIKeyGroup struct {
	ent.Schema
}

func (APIKeyGroup) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "api_key_groups"},
	}
}

func (APIKeyGroup) Mixin() []ent.Mixin {
	return nil
}

func (APIKeyGroup) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("api_key_id"),
		field.Int64("group_id"),
		field.Int("priority").
			Default(1).
			Comment("1 为主分组，数值越小越先尝试"),
		// 本表只保存当前生效的分组链，重建时整批替换，因此只需要 created_at。
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (APIKeyGroup) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("api_key", APIKey.Type).
			Unique().
			Required().
			Field("api_key_id"),
		edge.To("group", Group.Type).
			Unique().
			Required().
			Field("group_id"),
	}
}

func (APIKeyGroup) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("api_key_id", "group_id").Unique(),
		index.Fields("api_key_id", "priority"),
		index.Fields("group_id"),
	}
}
