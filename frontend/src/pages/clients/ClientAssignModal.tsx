import { useState } from 'react';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { Alert, Modal, Select, Space } from 'antd';
import { useTranslation } from 'react-i18next';
import { delegationPath, readDelegation, type PanelRole } from '@/routes/panelAccess';
import { HttpUtil } from '@/utils';

type Target = { id: number; username: string; role: PanelRole; hasCredit: boolean };
const assignmentTargetsKey = ['delegation', 'client-assignment-targets'] as const;
const roleLabels: Record<PanelRole, string> = {
  owner: 'delegation.owner', sudo: 'delegation.sudo', admin: 'delegation.admin',
  reseller: 'delegation.reseller', infinity: 'delegation.infinity',
};

export default function ClientAssignModal({ email, privileged, onClose, onAssigned }: {
  email: string;
  privileged: boolean;
  onClose: () => void;
  onAssigned: () => void;
}) {
  const { t } = useTranslation();
  const queryClient = useQueryClient();
  const [accountId, setAccountId] = useState<number>();
  const path = '/client-page';
  const encoded = encodeURIComponent(email);
  const targets = useQuery({
    queryKey: [...assignmentTargetsKey, email],
    queryFn: () => readDelegation<Target[]>(path + '/assign-targets/' + encoded),
    refetchOnMount: 'always',
    staleTime: 0,
    retry: false,
  });
  const transfer = useMutation({
    mutationFn: async (id: number) => {
      const result = await HttpUtil.post(delegationPath + path + '/assign/' + encoded, { accountId: id }, {
        headers: { 'Content-Type': 'application/json' },
        silent: true,
      });
      if (!result.success) throw new Error(result.msg);
    },
    onSuccess: () => {
      // Ownership and both accounts' balances can affect future assignments.
      void queryClient.invalidateQueries({ queryKey: assignmentTargetsKey, refetchType: 'none' });
      onAssigned();
    },
  });
  const errors: Record<string, string> = {
    insufficient_credit: 'delegation.insufficient_credit',
    assignment_inbound_unavailable: 'delegation.assignInboundUnavailable',
    not_found: 'delegation.not_found',
    forbidden: 'delegation.forbidden',
    invalid_input: 'delegation.invalid_input',
  };
  const error = transfer.error ?? targets.error;
  const ready = targets.isFetchedAfterMount && targets.isSuccess && !targets.isFetching;
  const options = ready ? targets.data : undefined;
  const canSubmit = options?.some((target) => target.id === accountId && target.hasCredit);

  return (
    <Modal
      open
      title={t('delegation.assignClient')}
      okText={t('delegation.assignClient')}
      cancelText={t('cancel')}
      confirmLoading={transfer.isPending}
      okButtonProps={{ disabled: !canSubmit }}
      cancelButtonProps={{ disabled: transfer.isPending }}
      closable={!transfer.isPending}
      maskClosable={false}
      keyboard={!transfer.isPending}
      onCancel={onClose}
      onOk={() => { if (accountId && canSubmit && !transfer.isPending) transfer.mutate(accountId); }}
    >
      <Space orientation="vertical" style={{ width: '100%' }}>
        <p>{t(privileged ? 'delegation.assignFreeHelp' : 'delegation.assignHelp')}</p>
        <Select
          aria-label={t('delegation.assignTarget')}
          placeholder={t('delegation.assignTarget')}
          style={{ width: '100%' }}
          showSearch={{ optionFilterProp: 'label' }}
          loading={targets.isFetching}
          disabled={transfer.isPending || !ready}
          value={accountId}
          onChange={(id) => { transfer.reset(); setAccountId(id); }}
          notFoundContent={t('delegation.assignNoTargets')}
          options={options?.map((target) => ({
            value: target.id,
            label: `${target.username} · ${t(roleLabels[target.role])}${target.hasCredit ? '' : ' · ' + t('delegation.insufficient_credit')}`,
            disabled: !target.hasCredit,
          }))}
        />
        {error && <Alert type="error" showIcon title={t(errors[error.message] ?? 'delegation.request_failed')} />}
      </Space>
    </Modal>
  );
}
