import { defineComponent, h } from "vue";
import { flushPromises, mount } from "@vue/test-utils";
import { createPinia } from "pinia";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AdminGroup } from "@/types";
import GroupsView from "@/views/admin/GroupsView.vue";

const {
  listGroups,
  getModelAllowlistCandidates,
  getUsageSummary,
  getCapacitySummary,
  getLiveCapability,
  createGroup,
  updateGroup,
} = vi.hoisted(() => ({
  listGroups: vi.fn(),
  getModelAllowlistCandidates: vi.fn(),
  getUsageSummary: vi.fn(),
  getCapacitySummary: vi.fn(),
  getLiveCapability: vi.fn(),
  createGroup: vi.fn(),
  updateGroup: vi.fn(),
}));

vi.mock("@/api/admin", () => ({
  adminAPI: {
    groups: {
      list: listGroups,
      getAll: vi.fn(),
      getModelAllowlistCandidates,
      getUsageSummary,
      getCapacitySummary,
      getLiveCapability,
      create: createGroup,
      update: updateGroup,
      delete: vi.fn(),
      duplicate: vi.fn(),
      updateSortOrder: vi.fn(),
    },
    accounts: {
      list: vi.fn(),
      getById: vi.fn(),
    },
  },
}));

vi.mock("@/stores/app", () => ({
  useAppStore: () => ({
    showError: vi.fn(),
    showSuccess: vi.fn(),
  }),
}));

vi.mock("@/stores/onboarding", () => ({
  useOnboardingStore: () => ({
    isCurrentStep: vi.fn(() => false),
    nextStep: vi.fn(),
  }),
}));

vi.mock("vue-i18n", async () => {
  const actual = await vi.importActual<typeof import("vue-i18n")>("vue-i18n");
  return {
    ...actual,
    useI18n: () => ({ t: (key: string) => key }),
  };
});

const sourceGroup = {
  id: 42,
  name: "OpenAI",
  description: null,
  platform: "openai",
  rate_multiplier: 1,
  upstream_rate_multiplier: 1.375,
  fee_calculator_sync_enabled: false,
  rpm_limit: 0,
  is_exclusive: false,
  status: "active",
  subscription_type: "standard",
  daily_limit_usd: null,
  weekly_limit_usd: null,
  monthly_limit_usd: null,
  long_context_pricing_enabled: true,
  force_openai_fast: false,
  force_openai_ultrafast: false,
  free_openai_fast: false,
  model_pricing: [],
  profit_control_enabled: false,
  profit_min_margin: 0,
  profit_safety_buffer: 0,
  allow_image_generation: false,
  allow_batch_image_generation: false,
  image_rate_independent: false,
  image_rate_multiplier: 1,
  batch_image_discount_multiplier: 0.5,
  batch_image_hold_multiplier: 0.6,
  image_price_1k: null,
  image_price_2k: null,
  image_price_4k: null,
  video_rate_independent: false,
  video_rate_multiplier: 1,
  video_price_480p: null,
  video_price_720p: null,
  video_price_1080p: null,
  web_search_price_per_call: null,
  search_price_per_1k: null,
  audio_realtime_price_per_min: null,
  audio_tts_price_per_million_chars: null,
  audio_stt_price_per_hour: null,
  peak_rate_enabled: false,
  peak_start: "",
  peak_end: "",
  peak_rate_multiplier: 1,
  claude_code_only: false,
  fallback_group_id: null,
  fallback_group_id_on_invalid_request: null,
  allow_messages_dispatch: false,
  allow_live: false,
  require_oauth_only: false,
  require_privacy_set: false,
  created_at: "2026-09-05T00:00:00Z",
  updated_at: "2026-09-05T00:00:00Z",
  model_routing: null,
  model_routing_enabled: false,
  mcp_xml_inject: true,
  supported_model_scopes: [],
  account_count: 1,
  active_account_count: 1,
  rate_limited_account_count: 0,
  models_list_config: undefined,
  codex_models_manifest_config: {
    enabled: false,
    account_ids: [],
    fallback_to_scheduler: false,
  },
  sort_order: 10,
} satisfies AdminGroup;

const AppLayoutStub = defineComponent({
  template: "<main><slot /></main>",
});

const TablePageLayoutStub = defineComponent({
  template: '<section><slot name="filters" /><slot name="table" /><slot name="pagination" /></section>',
});

const DataTableStub = defineComponent({
  props: {
    data: { type: Array, default: () => [] },
  },
  template: '<div><div v-for="row in data" :key="row.id" :data-row="row.id"><slot name="cell-actions" :row="row" /></div></div>',
});

const BaseDialogStub = defineComponent({
  props: {
    show: { type: Boolean, default: false },
  },
  template: '<div v-if="show"><slot /><slot name="footer" /></div>',
});

// 表单提交前会调用这些子组件的 validate()，自动桩不提供该方法。
const ValidatingFieldStub = defineComponent({
  setup(_, { expose }) {
    expose({ validate: () => true, resetValidation: () => undefined });
    return () => h("div");
  },
});

const mountView = () =>
  mount(GroupsView, {
    global: {
      plugins: [createPinia()],
      stubs: {
        AppLayout: AppLayoutStub,
        TablePageLayout: TablePageLayoutStub,
        DataTable: DataTableStub,
        Pagination: true,
        BaseDialog: BaseDialogStub,
        ConfirmDialog: true,
        EmptyState: true,
        Select: true,
        PlatformIcon: true,
        Icon: true,
        GroupCapacityBadge: true,
        GroupRateMultipliersModal: true,
        GroupRPMOverridesModal: true,
        ReasoningEffortPolicyFields: ValidatingFieldStub,
        CodexManifestAccountsField: ValidatingFieldStub,
        PricingEntryCard: true,
        VueDraggable: true,
      },
    },
  });

const mounted: ReturnType<typeof mountView>[] = [];

const render = async (groups: AdminGroup[] = []) => {
  listGroups.mockResolvedValue({ items: groups, total: groups.length, page: 1, page_size: 20, pages: 1 });
  const wrapper = mountView();
  mounted.push(wrapper);
  await flushPromises();
  return wrapper;
};

const findButton = (wrapper: ReturnType<typeof mountView>, text: string) => {
  const button = wrapper.findAll("button").find((item) => item.text().includes(text));
  expect(button, `button containing ${text}`).toBeDefined();
  return button!;
};

const openCreate = async (wrapper: ReturnType<typeof mountView>) => {
  await findButton(wrapper, "admin.groups.createGroup").trigger("click");
  await flushPromises();
  await wrapper.get('#create-group-form input[type="text"]').setValue("新分组");
};

const openEdit = async (wrapper: ReturnType<typeof mountView>, id: number) => {
  const row = wrapper.get(`[data-row="${id}"]`);
  const edit = row.findAll("button").find((item) => item.text().includes("common.edit"));
  expect(edit).toBeDefined();
  await edit!.trigger("click");
  await flushPromises();
};

const syncCheckbox = (wrapper: ReturnType<typeof mountView>, form: "create" | "edit") =>
  wrapper.get<HTMLInputElement>(`[data-testid="${form}-fee-calculator-sync"]`);

describe("GroupsView fee calculator synchronization", () => {
  beforeEach(() => {
    localStorage.clear();
    vi.clearAllMocks();
    getModelAllowlistCandidates.mockResolvedValue([]);
    getUsageSummary.mockResolvedValue([]);
    getCapacitySummary.mockResolvedValue([]);
    getLiveCapability.mockResolvedValue({ supported: false });
    createGroup.mockResolvedValue({});
    updateGroup.mockResolvedValue({});
  });

  afterEach(() => {
    mounted.splice(0).forEach((wrapper) => wrapper.unmount());
    localStorage.clear();
  });

  it("defaults new groups to unchecked and submits false", async () => {
    const wrapper = await render();
    await openCreate(wrapper);
    expect(syncCheckbox(wrapper, "create").element.checked).toBe(false);

    await wrapper.get("#create-group-form").trigger("submit");
    await flushPromises();
    expect(createGroup).toHaveBeenCalledTimes(1);
    expect(createGroup.mock.calls[0][0]).toMatchObject({ fee_calculator_sync_enabled: false });
  });

  it("saves an explicitly checked new group and resets the next create dialog to unchecked", async () => {
    const wrapper = await render();
    await openCreate(wrapper);
    await syncCheckbox(wrapper, "create").setValue(true);
    await wrapper.get("#create-group-form").trigger("submit");
    await flushPromises();
    expect(createGroup).toHaveBeenCalledTimes(1);
    expect(createGroup.mock.calls[0][0]).toMatchObject({ fee_calculator_sync_enabled: true });

    await openCreate(wrapper);
    expect(syncCheckbox(wrapper, "create").element.checked).toBe(false);
  });

  it("loads a saved enabled flag and submits false when it is unchecked", async () => {
    const wrapper = await render([{ ...sourceGroup, fee_calculator_sync_enabled: true }]);
    await openEdit(wrapper, sourceGroup.id);
    expect(syncCheckbox(wrapper, "edit").element.checked).toBe(true);

    await syncCheckbox(wrapper, "edit").setValue(false);
    await wrapper.get("#edit-group-form").trigger("submit");
    await flushPromises();
    expect(updateGroup).toHaveBeenCalledTimes(1);
    expect(updateGroup.mock.calls[0]).toEqual([
      sourceGroup.id,
      expect.objectContaining({ fee_calculator_sync_enabled: false }),
    ]);
  });

  it("keeps existing enabled configuration when saving other group fields", async () => {
    const wrapper = await render([{ ...sourceGroup, fee_calculator_sync_enabled: true }]);
    await openEdit(wrapper, sourceGroup.id);
    await wrapper.get('[data-testid="edit-upstream-rate-multiplier"]').setValue("0.2");
    await wrapper.get("#edit-group-form").trigger("submit");
    await flushPromises();
    expect(updateGroup.mock.calls[0][1]).toMatchObject({
      fee_calculator_sync_enabled: true,
      upstream_rate_multiplier: 0.2,
    });
  });

  it("does not carry an enabled flag into a different legacy group with no saved flag", async () => {
    const { fee_calculator_sync_enabled: _flag, ...legacyGroup } = sourceGroup;
    const wrapper = await render([
      { ...sourceGroup, fee_calculator_sync_enabled: true },
      { ...legacyGroup, id: 43, name: "旧分组" },
    ]);
    await openEdit(wrapper, sourceGroup.id);
    expect(syncCheckbox(wrapper, "edit").element.checked).toBe(true);
    await findButton(wrapper, "common.cancel").trigger("click");
    await flushPromises();

    await openEdit(wrapper, 43);
    expect(syncCheckbox(wrapper, "edit").element.checked).toBe(false);
    await wrapper.get("#edit-group-form").trigger("submit");
    await flushPromises();
    expect(updateGroup.mock.calls[0]).toEqual([
      43,
      expect.objectContaining({ fee_calculator_sync_enabled: false }),
    ]);
  });
});