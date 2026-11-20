import { useState } from "react";
import {
  Button,
  Alert,
  Card,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
} from "antd";
import { DeleteOutlined, EditOutlined, PlusOutlined, SettingOutlined, TeamOutlined } from "@ant-design/icons";
import { useNavigate } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { useTranslation } from "react-i18next";
import {
  panelAccountKey,
  usePanelAccount,
  type PanelAccount,
  type PanelRole,
} from "@/routes/panelAccess";
import {
  DelegationLoadError,
  DelegationShell,
  maxCredits,
  useDelegationData,
  useDelegationPost,
  type DelegatedInbound,
} from "./shared";

const roleKeys: Record<PanelRole, string> = {
  owner: "delegation.owner",
  sudo: "delegation.sudo",
  admin: "delegation.admin",
  reseller: "delegation.reseller",
  infinity: "delegation.infinity",
};

type AccountRow = PanelAccount & { children?: AccountRow[] };

function accountTree(accounts: PanelAccount[], viewerId?: number) {
  const rows = new Map(accounts.map((account) => [account.id, { ...account } as AccountRow]));
  const roots: AccountRow[] = [];
  for (const row of rows.values()) {
    const parent = row.parentId !== viewerId ? rows.get(row.parentId) : undefined;
    if (parent && parent.id !== row.id) (parent.children ??= []).push(row);
    else roots.push(row);
  }
  return { rows, roots };
}

export default function AdminsPage() {
  const { t } = useTranslation();
  return (
    <DelegationShell title={t("delegation.admins")} subtitle={t("delegation.adminsHint")}>
      <AdminsContent />
    </DelegationShell>
  );
}

function AdminsContent() {
  const { t } = useTranslation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const me = usePanelAccount().data;
  const accounts = useDelegationData<PanelAccount[]>("/accounts");
  const owner = me?.role === "owner";
  const post = useDelegationPost();
  const [creating, setCreating] = useState(false);
  const [creditFor, setCreditFor] = useState<PanelAccount>();
  const [inboundsFor, setInboundsFor] = useState<PanelAccount>();
  const [editing, setEditing] = useState<PanelAccount>();
  const [collapsed, setCollapsed] = useState<Set<number>>(() => new Set());
  const tree = accountTree(accounts.data ?? [], me?.id);
  const infinity = me?.role === "infinity";
  const balanceHint = !owner && me
    ? infinity ? t("delegation.infinityBalance") : t("delegation.balance", { clients: me.clients, gib: me.gib })
    : undefined;
  const canEditCredit = editing?.role !== "infinity" && (owner || editing?.parentId === me?.id);
  const canEditQuotaPolicy = !!editing && (owner || editing.parentId === me?.id) &&
    (editing.role === "admin" || editing.role === "reseller");
  const allocatable = owner || infinity ? maxCredits : me?.unlimitedClientAvailable ?? 0;
  const duplicateUsername = post.error?.message === "username_taken";
  const roles: PanelRole[] = owner
    ? ["sudo", "admin", "reseller", "infinity"]
    : infinity || me?.role === "sudo" ? ["admin", "reseller", "infinity"] : ["admin", "reseller"];
  const refresh = () => {
    void accounts.refetch();
    void qc.invalidateQueries({ queryKey: panelAccountKey });
  };

  return (
    <>
      <Card
        size="small"
        hoverable
        extra={
          <Button
            icon={<PlusOutlined />}
            type="primary"
            onClick={() => { post.reset(); setCreating(true); }}
          >
            {t("delegation.addAccount")}
          </Button>
        }
        title={
          balanceHint ?? t("delegation.accounts")
        }
      >
        {accounts.isError ? (
          <DelegationLoadError />
        ) : (
          <Table
            size="small"
            rowKey="id"
            scroll={{ x: 720 }}
            dataSource={tree.roots}
            expandable={{
              expandedRowKeys: [...tree.rows.values()].filter(row => row.children?.length && !collapsed.has(row.id)).map(row => row.id),
              onExpand: (expanded, row) => setCollapsed(previous => {
                const next = new Set(previous);
                if (expanded) next.delete(row.id);
                else next.add(row.id);
                return next;
              }),
            }}
            loading={accounts.isPending}
            columns={[
              { title: t("username"), dataIndex: "username" },
              {
                title: t("delegation.parentAccount"),
                render: (_, row) => (row.parentId === me?.id ? me?.username : tree.rows.get(row.parentId)?.username) ?? "—",
              },
              {
                title: t("delegation.role"),
                render: (_, row) => <Tag>{t(roleKeys[row.role])}</Tag>,
              },
              { title: t("delegation.clientCredits"), render: (_, row) => row.role === "infinity" || row.uncappedClientSlots ? "∞" : row.clients },
              { title: t("delegation.trafficCredits"), render: (_, row) => row.role === "infinity" ? "∞" : row.gib },
              { title: t("delegation.unlimitedClientLimit"), render: (_, row) => row.role === "infinity" ? "∞" : row.unlimitedClientLimit ?? 0 },
              {
                key: "actions",
                render: (_, row) => {
                  const direct = row.parentId === me?.id;
                  const canEdit = owner || ((me?.role === "admin" || me?.role === "sudo" || infinity) &&
                    (row.role === "admin" || row.role === "reseller")) ||
                    (row.role === "infinity" && (infinity || me?.role === "sudo"));
                  return (
                    <Space wrap>
                      {owner && <Tooltip title={t("menu.clients")}>
                        <Button type="text" icon={<TeamOutlined />} aria-label={t("menu.clients")}
                          onClick={() => navigate(`/clients?${new URLSearchParams({ search: `owner:${row.username}` })}`)} />
                      </Tooltip>}
                      {direct && row.role !== "infinity" && (
                        <Tooltip title={t("delegation.grant")}>
                          <Button type="text" icon={<PlusOutlined />} aria-label={t("delegation.grant")} onClick={() => setCreditFor(row)} />
                        </Tooltip>
                      )}
                      {(owner || direct) && (
                        <Tooltip title={t("delegation.manageInbounds")}>
                          <Button type="text" icon={<SettingOutlined />} aria-label={t("delegation.manageInbounds")} onClick={() => setInboundsFor(row)} />
                        </Tooltip>
                      )}
                      {canEdit && <>
                        <Tooltip title={t("edit")}>
                          <Button type="text" icon={<EditOutlined />} aria-label={t("edit")} onClick={() => { post.reset(); setEditing(row); }} />
                        </Tooltip>
                        <Popconfirm title={t("delegation.deleteAccountConfirm")}
                          description={row.username}
                          onConfirm={() => post.mutateAsync({ path: `/accounts/${row.id}/delete`, body: { cascade: true } }, { onSuccess: refresh }).catch(() => { refresh(); })}>
                          <Button type="text" danger icon={<DeleteOutlined />} aria-label={t("delete")} />
                        </Popconfirm>
                      </>}
                    </Space>
                  );
                },
              },
            ]}
          />
        )}
      </Card>
      {owner && <AvailableInbounds />}
      {inboundsFor && <AccountInbounds account={inboundsFor} onClose={() => setInboundsFor(undefined)} />}
      {editing && <Modal open destroyOnHidden title={`${t("edit")} · ${editing.username}`} footer={null} onCancel={() => setEditing(undefined)}>
        {canEditCredit && <Alert type="info" showIcon
          message={t(owner || infinity ? "delegation.editAccountHint" : "delegation.editDelegatedCreditHint")}
          description={balanceHint}
          style={{marginBottom: 16}} />}
        <Form layout="vertical" initialValues={{ username: editing.username, clients: editing.clients, gib: editing.gib,
          uncappedClientSlots: editing.uncappedClientSlots ?? false, unlimitedClientLimit: editing.unlimitedClientLimit ?? 0 }}
          onFinish={({ username, password, clients, gib, uncappedClientSlots, unlimitedClientLimit }: {
            username: string; password?: string; clients: number; gib: number; uncappedClientSlots: boolean; unlimitedClientLimit: number;
          }) => {
            const body = {
              username, password,
              ...(canEditCredit && clients !== editing.clients ? { clients } : {}),
              ...(canEditCredit && gib !== editing.gib ? { gib } : {}),
              ...(canEditQuotaPolicy && owner ? { uncappedClientSlots } : {}),
              ...(canEditQuotaPolicy && unlimitedClientLimit !== (editing.unlimitedClientLimit ?? 0) ? { unlimitedClientLimit } : {}),
            };
            post.mutate({ path: `/accounts/${editing.id}/edit`, body }, { onSuccess: () => {setEditing(undefined); refresh();} });
          }}>
          <Form.Item name="username" label={t("username")} rules={[{required: true, min: 3, max: 80}]}
            validateStatus={duplicateUsername ? "error" : undefined}
            help={duplicateUsername ? t("delegation.username_taken") : undefined}>
            <Input onChange={() => { if (duplicateUsername) post.reset(); }} />
          </Form.Item>
          <Form.Item name="password" label={t("password")} rules={[{min: 12, max: 72}]}><Input.Password autoComplete="new-password" /></Form.Item>
          {canEditCredit && <>
            <Form.Item name="clients" label={t("delegation.clientCredits")} rules={[{required: true}]}><InputNumber min={0} max={maxCredits} precision={0} /></Form.Item>
            <Form.Item name="gib" label={t("delegation.trafficCredits")} rules={[{required: true}]}><InputNumber min={0} max={maxCredits} precision={0} /></Form.Item>
          </>}
          {canEditQuotaPolicy && owner && (
            <Form.Item name="uncappedClientSlots" label={t("delegation.uncappedClientSlots")} valuePropName="checked">
              <Switch />
            </Form.Item>
          )}
          {canEditQuotaPolicy && (
            <Form.Item name="unlimitedClientLimit" label={t("delegation.unlimitedClientLimit")}
              extra={t("delegation.unlimitedClientLimitHint")} rules={[{required: true}]}>
              <InputNumber min={0} max={Math.min(maxCredits, allocatable + (editing.unlimitedClientLimit ?? 0))} precision={0} />
            </Form.Item>
          )}
          <Button type="primary" htmlType="submit" loading={post.isPending}>{t("save")}</Button>
        </Form>
      </Modal>}
      <Modal
        open={creating}
        destroyOnHidden
        title={t("delegation.addAccount")}
        footer={null}
        onCancel={() => setCreating(false)}
      >
        <Form
          layout="vertical"
          initialValues={{ role: infinity ? "infinity" : "admin", unlimitedClientLimit: 0 }}
          onFinish={(body: {
            username: string;
            password: string;
            role: PanelRole;
            unlimitedClientLimit?: number;
          }) => {
            post.mutate(
              { path: "/accounts", body: {
                ...body,
                unlimitedClientLimit: body.role === "admin" || body.role === "reseller" ? body.unlimitedClientLimit : undefined,
              } },
              {
                onSuccess: () => {
                  setCreating(false);
                  refresh();
                },
              },
            );
          }}
        >
          <Form.Item
            name="username"
            label={t("username")}
            rules={[{ required: true, min: 3, max: 80, whitespace: true }]}
            validateStatus={duplicateUsername ? "error" : undefined}
            help={duplicateUsername ? t("delegation.username_taken") : undefined}
          >
            <Input autoComplete="off" onChange={() => { if (duplicateUsername) post.reset(); }} />
          </Form.Item>
          <Form.Item
            name="password"
            label={t("password")}
            rules={[{ required: true, min: 12, max: 72 }]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            name="role"
            label={t("delegation.role")}
            rules={[{ required: true }]}
          >
            <Select
              options={roles.map((role) => ({
                value: role,
                label: t(roleKeys[role]),
              }))}
            />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(prev, next) => prev.role !== next.role}>
            {({ getFieldValue }) => ["admin", "reseller"].includes(getFieldValue("role")) && (
              <Form.Item name="unlimitedClientLimit" label={t("delegation.unlimitedClientLimit")}
                extra={t("delegation.unlimitedClientLimitHint")} rules={[{required: true}]}>
                <InputNumber min={0} max={allocatable} precision={0} />
              </Form.Item>
            )}
          </Form.Item>
          <Button type="primary" htmlType="submit" loading={post.isPending}>
            {t("delegation.addAccount")}
          </Button>
        </Form>
      </Modal>
      <Modal
        open={!!creditFor}
        destroyOnHidden
        title={`${t("delegation.grant")} · ${creditFor?.username ?? ""}`}
        footer={null}
        onCancel={() => setCreditFor(undefined)}
      >
        <Alert type="info" showIcon style={{ marginBottom: 16 }} message={t(infinity ? "delegation.infinityBalance" : "delegation.creditHint")}
          description={infinity ? undefined : balanceHint} />
        <Form
          layout="vertical"
          initialValues={{ clients: 0, gib: 0 }}
          onFinish={(body: { clients: number; gib: number }) => {
            if (creditFor)
              post.mutate(
                { path: `/accounts/${creditFor.id}/credit`, body },
                {
                  onSuccess: () => {
                    setCreditFor(undefined);
                    refresh();
                  },
                },
              );
          }}
        >
          <Space wrap>
            <Form.Item
              name="clients"
              label={t("delegation.clientCredits")}
              rules={[{ required: true }]}
            >
              <InputNumber min={0} max={owner || infinity ? maxCredits : Math.min(maxCredits, me?.clients ?? 0)} precision={0} />
            </Form.Item>
            <Form.Item
              name="gib"
              label={t("delegation.trafficCredits")}
              rules={[{ required: true }]}
            >
              <InputNumber min={0} max={owner || infinity ? maxCredits : Math.min(maxCredits, me?.gib ?? 0)} precision={0} />
            </Form.Item>
          </Space>
          <div>
            <Button type="primary" htmlType="submit" loading={post.isPending}>
              {t("delegation.grant")}
            </Button>
          </div>
        </Form>
      </Modal>
    </>
  );
}

function AccountInbounds({ account, onClose }: { account: PanelAccount; onClose: () => void }) {
  const { t } = useTranslation();
  const inbounds = useDelegationData<DelegatedInbound[]>(`/accounts/${account.id}/inbounds`);
  const post = useDelegationPost();
  return <Modal open title={`${t("delegation.manageInbounds")} · ${account.username}`} footer={null} onCancel={onClose}>
    <Alert type="info" showIcon message={t("delegation.accountInboundsHint")} style={{ marginBottom: 16 }} />
    {inbounds.isError ? <DelegationLoadError /> : <Table size="small" rowKey="id" loading={inbounds.isPending} dataSource={inbounds.data} columns={[
      { title: t("delegation.inbound"), render: (_, row) => row.remark || String(row.id) },
      { title: t("delegation.protocol"), dataIndex: "protocol" },
      { title: t("delegation.allowed"), render: (_, row) => <Switch checked={row.allowed} disabled={post.isPending}
        aria-label={`${t("delegation.allowed")}: ${row.remark || row.id}`}
        onChange={(allowed) => post.mutate({ path: `/accounts/${account.id}/inbounds/${row.id}`, body: { allowed } },
          { onSuccess: () => void inbounds.refetch() })} /> },
    ]} />}
  </Modal>;
}

function AvailableInbounds() {
  const { t } = useTranslation();
  const inbounds = useDelegationData<DelegatedInbound[]>("/inbounds");
  const post = useDelegationPost();
  return (
    <Card size="small" hoverable title={t("delegation.inbounds")} extra={t("delegation.inboundsHint")}>
      {inbounds.isError ? (
        <DelegationLoadError />
      ) : (
        <Table
          size="small"
          rowKey="id"
          loading={inbounds.isPending}
          dataSource={inbounds.data}
          columns={[
            {
              title: t("delegation.inbound"),
              render: (_, row) => row.remark || String(row.id),
            },
            { title: t("delegation.protocol"), dataIndex: "protocol" },
            {
              title: t("delegation.allowed"),
              render: (_, row) => (
                <Switch
                  checked={row.allowed}
                  disabled={post.isPending}
                  aria-label={`${t("delegation.allowed")}: ${row.remark || row.id}`}
                  onChange={(allowed) =>
                    post.mutate(
                      { path: `/inbounds/${row.id}`, body: { allowed } },
                      { onSuccess: () => void inbounds.refetch() },
                    )
                  }
                />
              ),
            },
          ]}
        />
      )}
    </Card>
  );
}
