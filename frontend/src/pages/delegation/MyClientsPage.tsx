import { useState } from 'react';
import { Alert, App, Button, Card, Modal, Space, Table, Tag, Typography } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { useTranslation } from 'react-i18next';
import { panelAccountKey, readDelegation, usePanelAccount } from '@/routes/panelAccess';
import { DelegationLoadError, DelegationShell, useDelegationData, type DelegatedInbound } from './shared';
import ClientTrafficCell from '@/components/clients/ClientTrafficCell';
import ClientFormModal from '@/pages/clients/ClientFormModal';
import { HttpUtil } from '@/utils';
import type { InboundOption } from '@/hooks/useClients';

type DelegatedClient = { email: string; subId: string; trafficBytes: number; enable: boolean; up: number; down: number };

export default function MyClientsPage() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  const qc = useQueryClient();
  const me = usePanelAccount().data;
  const clients = useDelegationData<DelegatedClient[]>('/clients');
  const inbounds = useDelegationData<DelegatedInbound[]>('/inbounds');
  const inboundOptions = useDelegationData<InboundOption[]>('/inbound-options');
  const [creating, setCreating] = useState(false);
  const [view, setView] = useState<DelegatedClient>();
  const links = useQuery({
    queryKey: ['delegation', 'links', view?.subId],
    queryFn: () => readDelegation<string[]>(`/clients/${encodeURIComponent(view!.subId)}/links`),
    enabled: !!view,
    retry: false,
  });
  const canCreate = !!inbounds.data?.length && !!me && me.clients > 0 && me.gib > 0;
  return (
    <DelegationShell title={t('delegation.myClients')} subtitle={t('delegation.clientsHint')}>
      <Card title={me && t('delegation.balance', { clients: me.clients, gib: me.gib })}
        extra={<Button icon={<PlusOutlined />} type="primary" disabled={!canCreate} onClick={() => setCreating(true)}>{t('delegation.addClient')}</Button>}>
        <Alert type="info" message={t('delegation.sharedFormLimits')} showIcon style={{ marginBottom: 16 }} />
        {inbounds.isError ? <DelegationLoadError /> : inbounds.data?.length === 0 &&
          <Alert type="info" message={t('delegation.noInbounds')} showIcon style={{ marginBottom: 16 }} />}
        {me && (me.clients <= 0 || me.gib <= 0) &&
          <Alert type="warning" message={t('delegation.noCredit')} showIcon style={{ marginBottom: 16 }} />}
        {clients.isError ? <DelegationLoadError /> : <Table
          rowKey="subId" scroll={{ x: 460 }} dataSource={clients.data} loading={clients.isPending}
          columns={[
            { title: t('delegation.clientName'), dataIndex: 'email' },
            { title: t('delegation.trafficCredits'), width: 300, render: (_, row) => <ClientTrafficCell up={row.up} down={row.down} total={row.trafficBytes} enabled={row.enable} /> },
            { title: t('delegation.status'), render: (_, row) => <Tag color={row.enable ? 'green' : 'default'}>{t(row.enable ? 'delegation.active' : 'delegation.inactive')}</Tag> },
            { render: (_, row) => <Button onClick={() => setView(row)}>{t('delegation.links')}</Button> },
          ]}
        />}
      </Card>
      {creating && <ClientFormModal open={creating} mode="add" client={null} serverAssignedSubId
        inbounds={inboundOptions.data ?? []} onOpenChange={setCreating}
        save={async (payload, meta) => {
          if (meta.externalLinks.length > 0) {
            message.error(t('delegation.sharedFormLimits'));
            return null;
          }
          const result = await HttpUtil.post('/panel/api/delegation/clients/full', payload, {
            headers: { 'Content-Type': 'application/json' }, silent: true,
          });
          if (result.success) {
            void clients.refetch();
            void qc.invalidateQueries({ queryKey: panelAccountKey });
          } else {
            const errorText = result.msg === 'insufficient_credit' ? t('delegation.insufficient_credit')
              : result.msg === 'invalid_input' ? t('delegation.sharedFormLimits')
              : result.msg === 'client_rejected' ? t('delegation.client_rejected') : t('delegation.request_failed');
            message.error(errorText);
          }
          return result;
        }} />}
      <Modal open={!!view} title={t('delegation.links')} footer={null} loading={links.isFetching} onCancel={() => setView(undefined)}>
        {links.isError ? <DelegationLoadError /> : <Space orientation="vertical" style={{ maxWidth: '100%' }}>
          {links.data?.length === 0 && <Typography.Text type="secondary">{t('delegation.noLinks')}</Typography.Text>}
          {links.data?.map((link) => <Typography.Text key={link} code copyable style={{ overflowWrap: 'anywhere' }}>{link}</Typography.Text>)}
        </Space>}
      </Modal>
    </DelegationShell>
  );
}
