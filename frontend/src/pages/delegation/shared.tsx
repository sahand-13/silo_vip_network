import type { ReactNode } from 'react';
import { useMutation, useQuery } from '@tanstack/react-query';
import { Alert, App, ConfigProvider, Layout, Space, Typography } from 'antd';
import { useTranslation } from 'react-i18next';
import AppSidebar from '@/layouts/AppSidebar';
import { useTheme } from '@/hooks/useTheme';
import { delegationPath, readDelegation } from '@/routes/panelAccess';
import { HttpUtil } from '@/utils';

export type DelegatedInbound = { id: number; remark: string; protocol: string; allowed: boolean };
export const maxCredits = 1_000_000;

export function useDelegationData<T>(path: string) {
  return useQuery({ queryKey: ['delegation', path], queryFn: () => readDelegation<T>(path), retry: false });
}

const errorKeys = new Map([
  ['forbidden', 'delegation.forbidden'],
  ['insufficient_credit', 'delegation.insufficient_credit'],
  ['credit_limit', 'delegation.credit_limit'],
  ['not_found', 'delegation.not_found'],
  ['invalid_input', 'delegation.invalid_input'],
  ['username_taken', 'delegation.username_taken'],
  ['client_rejected', 'delegation.client_rejected'],
  ['inbound_sync_failed', 'delegation.inboundSyncFailed'],
  ['account_has_dependents', 'delegation.accountHasDependents'],
]);

export function useDelegationPost() {
  const { t } = useTranslation();
  const { message } = App.useApp();
  return useMutation({
    mutationFn: async ({ path, body }: { path: string; body: unknown }) => {
      const msg = await HttpUtil.post(delegationPath + path, body, {
        headers: { 'Content-Type': 'application/json' },
        silent: true,
      });
      if (!msg.success) throw new Error(msg.msg);
    },
    onError: (e: Error, { path }) => {
      const fallback = path.endsWith('/delete') ? 'delegation.deleteAccountFailed' : 'delegation.request_failed';
      message.error(t(errorKeys.get(e.message) ?? fallback));
    },
  });
}

export function DelegationShell({ title, subtitle, children }: { title: string; subtitle: string; children: ReactNode }) {
  const { isDark, isUltra, antdThemeConfig } = useTheme();
  return (
    <ConfigProvider theme={antdThemeConfig}>
    <App>
    <Layout className={`clients-page${isDark ? ' is-dark' : ''}${isUltra ? ' is-ultra' : ''}`}>
      <AppSidebar />
      <Layout className="content-shell">
        <Layout.Content id="content-layout" className="content-area">
          <Space orientation="vertical" size="large" style={{ width: '100%' }}>
            <div>
              <Typography.Title level={2}>{title}</Typography.Title>
              <Typography.Text type="secondary">{subtitle}</Typography.Text>
            </div>
            {children}
          </Space>
        </Layout.Content>
      </Layout>
    </Layout>
    </App>
    </ConfigProvider>
  );
}

export function DelegationLoadError() {
  const { t } = useTranslation();
  return <Alert type="error" message={t('delegation.request_failed')} showIcon />;
}
